package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Game server image, pinned by the digest of its multi-arch index (release 2026.11.0, Java 25).
// Pulling by digest makes the content verified by the daemon; the tag is informational.
const (
	imageRepo   = "itzg/minecraft-server"
	imageTag    = "2026.11.0"
	imageDigest = "sha256:8d5080d8dcd0a2629c2e4f248678373a337a40537ad6ef2857c7dbcda6e3eadd"
	image       = imageRepo + "@" + imageDigest
)

// Fixed container policy.
const (
	gamePort           = "25565/tcp"
	gameBindIP         = "0.0.0.0"
	stopTimeoutSeconds = 60
	pidsLimit          = 2048
	containerPrefix    = "mesquita-"

	stopCallTimeout = (stopTimeoutSeconds + 15) * time.Second
	pullTimeout     = 10 * time.Minute
	maxPullStream   = 64 << 20
	maxInspectBytes = 256 << 10
)

// Runtime manages game server containers. Every method maps to one allowlisted Engine API
// endpoint, and every operation on an existing container first checks that the container
// carries mesquita.managed=true and the expected mesquita.server_id.
type Runtime struct {
	client   *Client
	user     string // uid:gid the game process runs as, so its files belong to the panel
	pullLock sync.Mutex
}

// NewRuntime runs containers as uid:gid (the panel's own user in production).
func NewRuntime(c *Client, uid, gid int) *Runtime {
	return &Runtime{client: c, user: fmt.Sprintf("%d:%d", uid, gid)}
}

// EnsureImage pulls the pinned image unless the daemon already has it.
func (r *Runtime) EnsureImage(ctx context.Context) error {
	r.pullLock.Lock()
	defer r.pullLock.Unlock()

	_, err := r.client.call(ctx, http.MethodGet, "/images/"+image+"/json", nil, defaultTimeout, 0, nil)
	var apiErr *apiError
	if err == nil {
		return nil
	}
	if !errors.As(err, &apiErr) || apiErr.status != http.StatusNotFound {
		return mapError("inspect image", err)
	}
	return r.pull(ctx)
}

// pull streams POST /images/create until EOF. Docker reports failures inside the stream
// (HTTP status stays 200), so every message is checked for an error.
func (r *Runtime) pull(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	q := url.Values{"fromImage": {imageRepo}, "tag": {imageDigest}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+apiVersion+"/images/create?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := r.client.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: pull image: %v", domain.ErrDockerUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mapError("pull image", readAPIError(resp))
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxPullStream))
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err == io.EOF {
			return nil
		} else if err != nil {
			return fmt.Errorf("%w: pull image: %v", domain.ErrDockerFailed, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("%w: pull image: %s", domain.ErrDockerFailed, msg.Error)
		}
	}
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type createRequest struct {
	Image        string              `json:"Image"`
	User         string              `json:"User"`
	Env          []string            `json:"Env"`
	Labels       map[string]string   `json:"Labels"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	OpenStdin    bool                `json:"OpenStdin"`
	StopTimeout  int                 `json:"StopTimeout"`
	HostConfig   hostConfig          `json:"HostConfig"`
}

type hostConfig struct {
	Binds         []string                 `json:"Binds"`
	PortBindings  map[string][]portBinding `json:"PortBindings"`
	Memory        int64                    `json:"Memory"`
	MemorySwap    int64                    `json:"MemorySwap"`
	NanoCPUs      int64                    `json:"NanoCpus"`
	PidsLimit     int64                    `json:"PidsLimit"`
	CapDrop       []string                 `json:"CapDrop"`
	SecurityOpt   []string                 `json:"SecurityOpt"`
	LogConfig     logConfig                `json:"LogConfig"`
	RestartPolicy struct {
		Name string `json:"Name"`
	} `json:"RestartPolicy"`
}

type logConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

// Create creates (does not start) the container of a server and returns its ID.
func (r *Runtime) Create(ctx context.Context, spec domain.ContainerSpec) (string, error) {
	if !domain.ValidServerID(spec.ServerID) {
		return "", fmt.Errorf("docker: invalid server id %q", spec.ServerID)
	}
	if !filepath.IsAbs(spec.DataDir) || strings.Contains(spec.DataDir, ":") {
		return "", fmt.Errorf("docker: invalid data dir %q", spec.DataDir)
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return "", fmt.Errorf("docker: invalid port %d", spec.Port)
	}

	body := createRequest{
		Image: image,
		User:  r.user,
		Env:   spec.Env,
		Labels: map[string]string{
			LabelManaged:  managedLabelValue,
			LabelServerID: spec.ServerID,
			LabelGame:     spec.Game,
		},
		ExposedPorts: map[string]struct{}{gamePort: {}},
		OpenStdin:    true, // the console (phase 4) attaches to stdin without recreating the container
		StopTimeout:  stopTimeoutSeconds,
		HostConfig: hostConfig{
			Binds:        []string{spec.DataDir + ":/data"},
			PortBindings: map[string][]portBinding{gamePort: {{HostIP: gameBindIP, HostPort: strconv.Itoa(spec.Port)}}},
			PidsLimit:    pidsLimit,
			CapDrop:      []string{"ALL"},
			SecurityOpt:  []string{"no-new-privileges"},
			LogConfig:    logConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
		},
	}
	body.HostConfig.Memory, body.HostConfig.MemorySwap, body.HostConfig.NanoCPUs = limits(spec.MemoryMB, spec.CPUMillicores)
	body.HostConfig.RestartPolicy.Name = "unless-stopped"

	var out struct {
		ID string `json:"Id"`
	}
	name := url.Values{"name": {ContainerName(spec.ServerID)}}.Encode()
	if _, err := r.client.call(ctx, http.MethodPost, "/containers/create?"+name, body, defaultTimeout, maxErrorBytes, &out); err != nil {
		return "", mapError("create container", err)
	}
	if !validContainerID(out.ID) {
		return "", fmt.Errorf("%w: create container: invalid id in response", domain.ErrDockerFailed)
	}
	return out.ID, nil
}

// ContainerName is the Docker name of a server's container.
func ContainerName(serverID string) string { return containerPrefix + strings.ToLower(serverID) }

// limits converts server limits to Engine API values. Swap equals memory: no swap at all.
func limits(memoryMB, cpuMillicores int) (memory, swap, nanoCPUs int64) {
	memory = int64(memoryMB) << 20
	return memory, memory, int64(cpuMillicores) * 1_000_000
}

type inspectResponse struct {
	ID    string `json:"Id"`
	State struct {
		Status string `json:"Status"`
		Health *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

// Inspect returns the state of a server's container after checking that it belongs to it.
func (r *Runtime) Inspect(ctx context.Context, id, serverID string) (domain.ContainerState, error) {
	if !validContainerID(id) {
		return domain.ContainerState{}, domain.ErrContainerMissing
	}
	var x inspectResponse
	if _, err := r.client.call(ctx, http.MethodGet, "/containers/"+id+"/json", nil, defaultTimeout, maxInspectBytes, &x); err != nil {
		return domain.ContainerState{}, mapError("inspect container", err)
	}
	if x.ID != id || x.Config.Labels[LabelManaged] != managedLabelValue || x.Config.Labels[LabelServerID] != serverID {
		return domain.ContainerState{}, fmt.Errorf("%w: container %s, server %s", domain.ErrNotManaged, id, serverID)
	}
	st := domain.ContainerState{ID: id, ServerID: serverID, State: x.State.Status}
	if x.State.Health != nil {
		st.Health = x.State.Health.Status
	}
	return st, nil
}

// States lists the state of every managed container.
func (r *Runtime) States(ctx context.Context) ([]domain.ContainerState, error) {
	list, err := r.client.listManaged(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ContainerState, 0, len(list))
	for _, c := range list {
		st := domain.ContainerState{ID: c.ID, ServerID: c.Labels[LabelServerID], State: c.State}
		// The list endpoint has no health field; its documented Status text carries it.
		if strings.Contains(c.Status, "(health: starting)") {
			st.Health = "starting"
		}
		out = append(out, st)
	}
	return out, nil
}

func (r *Runtime) Start(ctx context.Context, id, serverID string) error {
	return r.action(ctx, id, serverID, "start", "/start", defaultTimeout, 0)
}

// Stop asks the game to stop (SIGTERM, which the image turns into a "stop" command so the
// world is saved) and kills it after the stop timeout.
func (r *Runtime) Stop(ctx context.Context, id, serverID string) error {
	return r.action(ctx, id, serverID, "stop", "/stop?t="+strconv.Itoa(stopTimeoutSeconds), stopCallTimeout, 0)
}

func (r *Runtime) Restart(ctx context.Context, id, serverID string) error {
	return r.action(ctx, id, serverID, "restart", "/restart?t="+strconv.Itoa(stopTimeoutSeconds), stopCallTimeout, 0)
}

// Kill sends SIGKILL. Killing a container that is not running (409) is not an error.
func (r *Runtime) Kill(ctx context.Context, id, serverID string) error {
	return r.action(ctx, id, serverID, "kill", "/kill", defaultTimeout, http.StatusConflict)
}

// Remove force-removes the container (its volumes are bind mounts and are kept).
// A container that no longer exists is not an error.
func (r *Runtime) Remove(ctx context.Context, id, serverID string) error {
	if _, err := r.Inspect(ctx, id, serverID); errors.Is(err, domain.ErrContainerMissing) {
		return nil
	} else if err != nil {
		return err
	}
	_, err := r.client.call(ctx, http.MethodDelete, "/containers/"+id+"?force=true", nil, stopCallTimeout, 0, nil)
	if err = mapError("remove container", err); errors.Is(err, domain.ErrContainerMissing) {
		return nil
	}
	return err
}

// UpdateLimits changes memory and CPU limits in place.
func (r *Runtime) UpdateLimits(ctx context.Context, id, serverID string, memoryMB, cpuMillicores int) error {
	if _, err := r.Inspect(ctx, id, serverID); err != nil {
		return err
	}
	var body struct {
		Memory     int64 `json:"Memory"`
		MemorySwap int64 `json:"MemorySwap"`
		NanoCPUs   int64 `json:"NanoCpus"`
	}
	body.Memory, body.MemorySwap, body.NanoCPUs = limits(memoryMB, cpuMillicores)
	_, err := r.client.call(ctx, http.MethodPost, "/containers/"+id+"/update", body, defaultTimeout, maxErrorBytes, &struct{}{})
	return mapError("update container", err)
}

// action checks ownership, then POSTs to /containers/{id}{suffix}. okStatus, when not
// zero, is an error status that means the container is already in the wanted state.
func (r *Runtime) action(ctx context.Context, id, serverID, op, suffix string, timeout time.Duration, okStatus int) error {
	if _, err := r.Inspect(ctx, id, serverID); err != nil {
		return err
	}
	_, err := r.client.call(ctx, http.MethodPost, "/containers/"+id+suffix, nil, timeout, 0, nil)
	var apiErr *apiError
	if okStatus != 0 && errors.As(err, &apiErr) && apiErr.status == okStatus {
		return nil
	}
	return mapError(op+" container", err)
}

// mapError translates daemon errors to domain errors. Messages from the daemon are kept for
// the logs; the HTTP layer never returns them to clients.
func mapError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, domain.ErrDockerUnavailable) {
		return err
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		msg := strings.ToLower(apiErr.message)
		switch {
		case apiErr.status == http.StatusNotFound && strings.Contains(msg, "container"):
			return fmt.Errorf("%w: %s: %s", domain.ErrContainerMissing, op, apiErr.message)
		case strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use"):
			return fmt.Errorf("%w: %s: %s", domain.ErrPortInUse, op, apiErr.message)
		}
	}
	return fmt.Errorf("%w: %s: %v", domain.ErrDockerFailed, op, err)
}
