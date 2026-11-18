package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const serverID = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// lifecycleDaemon fakes the Engine API endpoints used by Runtime. Responses are keyed by
// "METHOD path"; requests are recorded with their bodies.
type lifecycleDaemon struct {
	mu        sync.Mutex
	responses map[string]fakeResponse
	requests  []string
	bodies    map[string]string
	queries   map[string]string
}

type fakeResponse struct {
	status int
	body   string
}

func newLifecycleDaemon() *lifecycleDaemon {
	return &lifecycleDaemon{responses: map[string]fakeResponse{}, bodies: map[string]string{}, queries: map[string]string{}}
}

func (d *lifecycleDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/v1.44")
	d.requests = append(d.requests, key)
	b, _ := io.ReadAll(r.Body)
	d.bodies[key], d.queries[key] = string(b), r.URL.RawQuery
	resp, ok := d.responses[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"No such container: x"}`)
		return
	}
	w.WriteHeader(resp.status)
	io.WriteString(w, resp.body)
}

func (d *lifecycleDaemon) on(key string, status int, body string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.responses[key] = fakeResponse{status, body}
}

func (d *lifecycleDaemon) sent(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.requests {
		if r == key {
			return true
		}
	}
	return false
}

func inspectJSON(id, state string, labels map[string]string) string {
	b, _ := json.Marshal(map[string]any{"Id": id, "State": map[string]any{"Status": state}, "Config": map[string]any{"Labels": labels}})
	return string(b)
}

func ours() map[string]string {
	return map[string]string{LabelManaged: "true", LabelServerID: serverID}
}

func newRuntime(t *testing.T) (*Runtime, *lifecycleDaemon) {
	d := newLifecycleDaemon()
	return NewRuntime(NewClient(startFake(t, d)), 998, 997), d
}

func TestCreateContainerPolicy(t *testing.T) {
	r, d := newRuntime(t)
	d.on("POST /containers/create", http.StatusCreated, `{"Id":"`+idRunning+`","Warnings":[]}`)

	id, err := r.Create(context.Background(), domain.ContainerSpec{
		ServerID: serverID, Game: "minecraft", Java: domain.Java17, Env: []string{"EULA=TRUE"}, DataDir: "/var/lib/mesquita-panel/servers/" + serverID,
		Port: 25601, MemoryMB: 2048, CPUMillicores: 1500,
	})
	if err != nil || id != idRunning {
		t.Fatalf("Create = %q, %v", id, err)
	}
	if q := d.queries["POST /containers/create"]; q != "name=mesquita-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("query = %q", q)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(d.bodies["POST /containers/create"]), &got); err != nil {
		t.Fatal(err)
	}
	hc := got["HostConfig"].(map[string]any)
	checks := map[string][2]any{
		"image":        {got["Image"], "itzg/minecraft-server@sha256:35f069ed801f4b14655acb52fdb7745dccfe69399867f57608ae4136a32c3c9e"},
		"user":         {got["User"], "998:997"},
		"open stdin":   {got["OpenStdin"], true},
		"stop timeout": {got["StopTimeout"], float64(60)},
		"memory":       {hc["Memory"], float64(2048 << 20)},
		"no swap":      {hc["MemorySwap"], float64(2048 << 20)},
		"cpus":         {hc["NanoCpus"], float64(1_500_000_000)},
		"pids":         {hc["PidsLimit"], float64(2048)},
		"restart":      {hc["RestartPolicy"].(map[string]any)["Name"], "unless-stopped"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %v, want %v", name, c[0], c[1])
		}
	}
	mustJSON := func(name string, v any, want string) {
		b, _ := json.Marshal(v)
		if string(b) != want {
			t.Errorf("%s = %s, want %s", name, b, want)
		}
	}
	mustJSON("labels", got["Labels"], `{"mesquita.game":"minecraft","mesquita.managed":"true","mesquita.server_id":"`+serverID+`"}`)
	mustJSON("binds", hc["Binds"], `["/var/lib/mesquita-panel/servers/`+serverID+`:/data"]`)
	mustJSON("ports", hc["PortBindings"], `{"25565/tcp":[{"HostIp":"0.0.0.0","HostPort":"25601"}]}`)
	mustJSON("cap drop", hc["CapDrop"], `["ALL"]`)
	mustJSON("security", hc["SecurityOpt"], `["no-new-privileges"]`)
	mustJSON("logs", hc["LogConfig"], `{"Config":{"max-file":"3","max-size":"10m"},"Type":"json-file"}`)
	if _, privileged := hc["Privileged"]; privileged {
		t.Error("Privileged must never be sent")
	}
}

func TestCreateRejectsBadSpecs(t *testing.T) {
	r, d := newRuntime(t)
	good := domain.ContainerSpec{ServerID: serverID, Java: domain.Java25, DataDir: "/data/x", Port: 25601, MemoryMB: 512, CPUMillicores: 500}
	for name, mutate := range map[string]func(*domain.ContainerSpec){
		"server id":     func(s *domain.ContainerSpec) { s.ServerID = "../x" },
		"relative dir":  func(s *domain.ContainerSpec) { s.DataDir = "data" },
		"colon in dir":  func(s *domain.ContainerSpec) { s.DataDir = "/data:/etc" },
		"invalid port":  func(s *domain.ContainerSpec) { s.Port = 0 },
		"too high port": func(s *domain.ContainerSpec) { s.Port = 70000 },
		"no java":       func(s *domain.ContainerSpec) { s.Java = "" },
		"unknown java":  func(s *domain.ContainerSpec) { s.Java = "java11" },
	} {
		spec := good
		mutate(&spec)
		if _, err := r.Create(context.Background(), spec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(d.requests) != 0 {
		t.Fatalf("invalid specs reached the daemon: %v", d.requests)
	}
}

func TestLifecycleChecksOwnership(t *testing.T) {
	ctx := context.Background()
	base := "/containers/" + idRunning

	r, d := newRuntime(t)
	d.on("GET "+base+"/json", http.StatusOK, inspectJSON(idRunning, "exited", ours()))
	d.on("POST "+base+"/start", http.StatusNoContent, "")
	d.on("POST "+base+"/stop", http.StatusNotModified, "") // already stopped is fine
	d.on("POST "+base+"/kill", http.StatusConflict, `{"message":"container is not running"}`)
	d.on("POST "+base+"/restart", http.StatusNoContent, "")
	d.on("DELETE "+base, http.StatusNoContent, "")
	d.on("POST "+base+"/update", http.StatusOK, `{"Warnings":[]}`)

	for name, op := range map[string]func() error{
		"start":   func() error { return r.Start(ctx, idRunning, serverID) },
		"stop":    func() error { return r.Stop(ctx, idRunning, serverID) },
		"kill":    func() error { return r.Kill(ctx, idRunning, serverID) },
		"restart": func() error { return r.Restart(ctx, idRunning, serverID) },
		"remove":  func() error { return r.Remove(ctx, idRunning, serverID) },
		"update":  func() error { return r.UpdateLimits(ctx, idRunning, serverID, 1024, 1000) },
	} {
		if err := op(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if q := d.queries["POST "+base+"/stop"]; q != "t=60" {
		t.Errorf("stop query = %q", q)
	}
	if q := d.queries["DELETE "+base]; q != "force=true" {
		t.Errorf("remove query = %q", q)
	}
	if b := d.bodies["POST "+base+"/update"]; b != `{"Memory":1073741824,"MemorySwap":1073741824,"NanoCpus":1000000000}` {
		t.Errorf("update body = %s", b)
	}

	// Containers that are not ours: never touched.
	for name, labels := range map[string]map[string]string{
		"other server": {LabelManaged: "true", LabelServerID: "ZZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		"not managed":  {LabelServerID: serverID},
	} {
		r, d := newRuntime(t)
		d.on("GET "+base+"/json", http.StatusOK, inspectJSON(idRunning, "running", labels))
		d.on("POST "+base+"/stop", http.StatusNoContent, "")
		d.on("DELETE "+base, http.StatusNoContent, "")
		if err := r.Stop(ctx, idRunning, serverID); !errors.Is(err, domain.ErrNotManaged) {
			t.Errorf("%s: stop err = %v", name, err)
		}
		if err := r.Remove(ctx, idRunning, serverID); !errors.Is(err, domain.ErrNotManaged) {
			t.Errorf("%s: remove err = %v", name, err)
		}
		if d.sent("POST "+base+"/stop") || d.sent("DELETE "+base) {
			t.Errorf("%s: mutating request sent", name)
		}
	}

	if err := r.Start(ctx, "../../images", serverID); !errors.Is(err, domain.ErrContainerMissing) {
		t.Errorf("invalid container id: %v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	ctx := context.Background()
	base := "/containers/" + idRunning

	r, d := newRuntime(t) // nothing registered: inspect answers 404 No such container
	if err := r.Start(ctx, idRunning, serverID); !errors.Is(err, domain.ErrContainerMissing) {
		t.Errorf("missing: %v", err)
	}
	if err := r.Remove(ctx, idRunning, serverID); err != nil {
		t.Errorf("removing a missing container: %v", err)
	}

	d.on("GET "+base+"/json", http.StatusOK, inspectJSON(idRunning, "created", ours()))
	d.on("POST "+base+"/start", http.StatusInternalServerError,
		`{"message":"driver failed programming external connectivity: Bind for 0.0.0.0:25601 failed: port is already allocated"}`)
	if err := r.Start(ctx, idRunning, serverID); !errors.Is(err, domain.ErrPortInUse) {
		t.Errorf("port: %v", err)
	}
	d.on("POST "+base+"/restart", http.StatusInternalServerError, `{"message":"something else"}`)
	if err := r.Restart(ctx, idRunning, serverID); !errors.Is(err, domain.ErrDockerFailed) {
		t.Errorf("generic: %v", err)
	}

	gone := NewRuntime(NewClient(filepath.Join(t.TempDir(), "none.sock")), 1, 1)
	if err := gone.Start(ctx, idRunning, serverID); !errors.Is(err, domain.ErrDockerUnavailable) {
		t.Errorf("no daemon: %v", err)
	}
	if _, err := gone.States(ctx); !errors.Is(err, domain.ErrDockerUnavailable) {
		t.Errorf("no daemon states: %v", err)
	}
}

func TestEnsureImage(t *testing.T) {
	ctx := context.Background()
	digest := gameImages[domain.Java21].digest
	inspect := "GET /images/itzg/minecraft-server@" + digest + "/json"

	r, d := newRuntime(t)
	d.on(inspect, http.StatusOK, `{}`)
	if err := r.EnsureImage(ctx, domain.Java21); err != nil || d.sent("POST /images/create") {
		t.Fatalf("present image: err=%v pulled=%v", err, d.sent("POST /images/create"))
	}

	r, d = newRuntime(t)
	d.on(inspect, http.StatusNotFound, `{"message":"No such image"}`)
	d.on("POST /images/create", http.StatusOK, `{"status":"Pulling"}`+"\n"+`{"status":"Downloaded newer image"}`+"\n")
	if err := r.EnsureImage(ctx, domain.Java21); err != nil {
		t.Fatal(err)
	}
	if q := d.queries["POST /images/create"]; q != "fromImage=itzg%2Fminecraft-server&tag="+strings.Replace(digest, ":", "%3A", 1) {
		t.Fatalf("pull query = %q", q)
	}

	r, d = newRuntime(t)
	d.on(inspect, http.StatusNotFound, `{"message":"No such image"}`)
	d.on("POST /images/create", http.StatusOK, `{"status":"Pulling"}`+"\n"+`{"error":"manifest unknown"}`+"\n")
	if err := r.EnsureImage(ctx, domain.Java21); !errors.Is(err, domain.ErrDockerFailed) {
		t.Fatalf("pull error in stream: %v", err)
	}

	// Only the pinned runtimes exist; anything else never reaches the daemon.
	r, d = newRuntime(t)
	if err := r.EnsureImage(ctx, "java11"); err == nil || len(d.requests) != 0 {
		t.Fatalf("unknown java: err=%v requests=%v", err, d.requests)
	}
	for java, img := range gameImages {
		if !strings.HasPrefix(img.digest, "sha256:") || len(img.digest) != 71 {
			t.Errorf("%s: bad digest %q", java, img.digest)
		}
	}
}

func TestStates(t *testing.T) {
	f := &fakeDaemon{list: listJSON(
		map[string]any{"Id": idRunning, "State": "running", "Status": "Up 3 seconds (health: starting)", "Labels": map[string]string{LabelManaged: "true", LabelServerID: serverID}},
		map[string]any{"Id": idStopped, "State": "exited", "Status": "Exited (0)", "Labels": map[string]string{LabelManaged: "true", LabelServerID: "X"}},
		map[string]any{"Id": idForeign, "State": "running", "Labels": map[string]string{}}, // filter bypassed: dropped
	)}
	r := NewRuntime(NewClient(startFake(t, f)), 1, 1)
	states, err := r.States(context.Background())
	if err != nil || len(states) != 2 {
		t.Fatalf("States = %+v, %v", states, err)
	}
	if states[0].Status() != domain.StatusStarting || states[0].ServerID != serverID || states[1].Status() != domain.StatusStopped {
		t.Fatalf("states = %+v", states)
	}
}

func TestInspectReadsRestartCount(t *testing.T) {
	r, d := newRuntime(t)
	d.on("GET /containers/"+idRunning+"/json", http.StatusOK,
		`{"Id":"`+idRunning+`","RestartCount":4,"State":{"Status":"restarting"},"Config":{"Labels":{"`+LabelManaged+`":"true","`+LabelServerID+`":"`+serverID+`"}}}`)
	st, err := r.Inspect(context.Background(), idRunning, serverID)
	if err != nil || st.RestartCount != 4 || st.Status() != domain.StatusRestarting {
		t.Fatalf("inspect = %+v, %v", st, err)
	}
}
