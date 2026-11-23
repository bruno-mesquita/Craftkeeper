// Command panel runs the Craftkeeper server. This is the only composition root:
// it loads configuration, builds adapters and injects them into the application layer.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/activity"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/auth"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/backups"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/console"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/dashboard"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/files"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/installers"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/maintenance"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/modpacks"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/mods"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/monitoring"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/players"
	"github.com/bruno-mesquita/Craftkeeper/internal/application/servers"
	"github.com/bruno-mesquita/Craftkeeper/internal/config"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/curseforge"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/docker"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/filesystem"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/gameversions"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/hostnet"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/metrics"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/minecraft"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/modrinth"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/mojang"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/password"
	"github.com/bruno-mesquita/Craftkeeper/internal/infrastructure/sqlite"
	httpapi "github.com/bruno-mesquita/Craftkeeper/internal/presentation/http"
	wspres "github.com/bruno-mesquita/Craftkeeper/internal/presentation/websocket"
	"github.com/bruno-mesquita/Craftkeeper/migrations"
	"github.com/bruno-mesquita/Craftkeeper/web"
)

const (
	metricsCacheTTL = 2 * time.Second
	versionsTTL     = time.Hour // official version lists change a few times a day at most
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "craftkeeper:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Setup workflow (spec §15): filesystem, database + migrations, Docker connectivity.
	if err := ensureWritableDir(cfg.DataDir); err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, cfg.DatabasePath(), migrations.FS)
	if err != nil {
		return err
	}
	defer store.Close()

	dockerClient := docker.NewClient(cfg.DockerSocket)
	if err := dockerClient.Ping(ctx); err != nil {
		log.Warn("docker is not reachable; server management will be unavailable", "socket", cfg.DockerSocket, "err", err)
	} else {
		log.Info("docker is reachable", "socket", cfg.DockerSocket)
	}

	users := sqlite.NewUsers(store)
	auditLog := sqlite.NewAuditLog(store, log)

	setupToken := ""
	if done, err := users.SetupCompleted(ctx); err != nil {
		return err
	} else if !done {
		setupToken = rand.Text()
		log.Warn("initial setup required: open the panel and enter this one-time setup token",
			"setup_token", setupToken)
	}

	authSvc, err := auth.NewService(users, sqlite.NewSessions(store), password.NewArgon2id(password.DefaultParams), auditLog,
		auth.Config{SetupToken: setupToken})
	if err != nil {
		return err
	}
	monitoringSvc := monitoring.NewService(metrics.NewLinux("/proc", cfg.DataDir), docker.NewStatsCollector(dockerClient, log), metricsCacheTTL)
	uid, gid := containerUser(log)
	containers := docker.NewRuntime(dockerClient, uid, gid)
	installersSvc := installers.NewService(gameversions.New(gameversions.Official), versionsTTL)
	settings := sqlite.NewSettings(store)
	serversSvc := servers.NewService(sqlite.NewServers(store), containers,
		filesystem.NewServerDirs(cfg.ServersDir(), cfg.BackupsDir(), uid, gid), installersSvc, auditLog, servers.Config{
			FirstPort: cfg.GamePorts.First, LastPort: cfg.GamePorts.Last,
			HostCPUs: runtime.NumCPU(), PortFree: hostnet.TCPPortFree, CurseForgeKey: settings.CurseForgeKey,
		})
	// Before serving: forget creations a previous run did not finish. Creations started from
	// now on run in the background and are interrupted (then recovered) at shutdown.
	if err := serversSvc.Recover(ctx); err != nil {
		return err
	}
	defer serversSvc.Close()
	// The crash guard stops servers stuck in a restart loop; it ends before Close waits.
	crashGuardDone := make(chan struct{})
	go func() {
		defer close(crashGuardDone)
		serversSvc.Run(ctx, dockerClient, log)
	}()
	defer func() {
		stop()
		<-crashGuardDone
	}()
	dashboardSvc := dashboard.NewService(dockerClient, serversSvc, auditLog)
	serverFiles := filesystem.NewServerFiles(cfg.ServersDir(), uid, gid)
	filesSvc := files.NewService(serverFiles, serversSvc, auditLog)

	// The console's event loop lives as long as the process; its feeds as long as viewers.
	consoleSvc := console.NewService(containers, dockerClient, serversSvc, auditLog, log)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		consoleSvc.Run(ctx)
	}()
	defer func() {
		stop() // ends Run when serve failed without a signal
		<-eventsDone
		consoleSvc.Close()
	}()
	playersSvc := players.NewService(serverFiles, serversSvc, consoleSvc, mojang.New(mojang.Official), minecraft.New(), auditLog)
	hub := wspres.NewHub(authSvc, consoleSvc, log)

	// Backups pause a running game's saving through the console, so they end before it.
	backupsSvc := backups.NewService(sqlite.NewBackups(store), filesystem.NewBackups(cfg.BackupsDir(), serverFiles),
		serversSvc, consoleSvc, auditLog, log)
	if err := backupsSvc.Recover(ctx); err != nil {
		return err
	}
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		backupsSvc.Run(ctx)
	}()
	defer func() {
		stop()
		<-schedulerDone
		backupsSvc.Close()
	}()

	maintenanceSvc := maintenance.NewService(serversSvc, backupsSvc, serverFiles, auditLog)
	modrinthClient := modrinth.New(modrinth.Official)
	modsSvc := mods.NewService(modrinthClient, serverFiles, serversSvc, backupsSvc, auditLog)
	modpacksSvc := modpacks.NewService(modrinthClient, curseforge.New(curseforge.Official), settings, serversSvc,
		maintenanceSvc, auditLog)

	handler := httpapi.New(authSvc, monitoringSvc, dashboardSvc, activity.NewService(auditLog), serversSvc, installersSvc,
		filesSvc, backupsSvc, playersSvc, maintenanceSvc, modsSvc, modpacksSvc, hub, web.FS, cfg.SecureCookie, log).Handler()
	return serve(ctx, log, cfg.Listen, handler, hub)
}

// serve binds every configured address before accepting traffic, then shuts all
// servers down gracefully when ctx is cancelled or any server fails. WebSocket connections
// are hijacked, so http.Server.Shutdown does not wait for them: the hub closes them after.
func serve(ctx context.Context, log *slog.Logger, addrs []string, handler http.Handler, hub *wspres.Hub) error {
	var listeners []net.Listener
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		listeners = append(listeners, ln)
	}

	errs := make(chan error, len(listeners))
	servers := make([]*http.Server, 0, len(listeners))
	for _, ln := range listeners {
		srv := &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    16 << 10,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		}
		servers = append(servers, srv)
		log.Info("listening", "addr", ln.Addr().String())
		go func() {
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-errs:
		log.Error("server failed", "err", runErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("graceful shutdown incomplete", "err", err)
		}
	}
	if err := hub.Shutdown(shutdownCtx); err != nil {
		log.Warn("websocket connections not closed in time", "err", err)
	}
	return runErr
}

// fallbackContainerUID/GID is the unprivileged user of the game image, used when the panel
// runs as root (development under sudo).
const fallbackContainerUID, fallbackContainerGID = 1000, 1000

// containerUser is the uid:gid game containers run as: the panel's own user, so server files
// belong to it. Game containers never run as root: without capabilities the image's root
// entrypoint cannot drop privileges, and root in a container is needless exposure.
func containerUser(log *slog.Logger) (uid, gid int) {
	if os.Getuid() != 0 {
		return os.Getuid(), os.Getgid()
	}
	log.Warn("running as root: game containers run as the image's default user instead; use a dedicated user in production",
		"uid", fallbackContainerUID, "gid", fallbackContainerGID)
	return fallbackContainerUID, fallbackContainerGID
}

func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("data dir %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("data dir %s is not writable: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
