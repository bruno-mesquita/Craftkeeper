// Package modpacks finds modpacks on Modrinth and CurseForge, resolves the version a new
// server runs and updates a server to another version of its pack. The game image installs
// and upgrades the pack itself; the panel only reads the catalogs. The CurseForge API key the
// catalog needs is stored in the panel's settings and never returned.
package modpacks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/servers"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// Modrinth reads Modrinth (modrinth.Client).
type Modrinth interface {
	SearchModpacks(ctx context.Context, query string, offset int) (domain.ModSearch, error)
	Projects(ctx context.Context, ids []string) (map[string]domain.ModProject, error)
	ProjectVersions(ctx context.Context, projectID, loader, mc string) ([]domain.ModVersion, error)
	Version(ctx context.Context, id string) (domain.ModVersion, error)
	Icon(ctx context.Context, url string) ([]byte, string, error)
}

// CurseForge reads CurseForge with the user's API key (curseforge.Client).
type CurseForge interface {
	Check(ctx context.Context, key string) error
	SearchModpacks(ctx context.Context, key, query string, offset int) (domain.ModSearch, error)
	Modpack(ctx context.Context, key, id string) (domain.ModProject, error)
	Versions(ctx context.Context, key, id string) ([]domain.ModpackVersion, error)
	Version(ctx context.Context, key, id, fileID string) (domain.ModpackVersion, error)
	Icon(ctx context.Context, url string) ([]byte, string, error)
}

// Settings stores the CurseForge API key (sqlite.Settings).
type Settings interface {
	CurseForgeKey(ctx context.Context) (string, error)
	SetCurseForgeKey(ctx context.Context, key string) error
}

// Servers resolves servers (servers.Service).
type Servers interface {
	Get(ctx context.Context, id string) (servers.View, error)
}

// Maintenance backs a stopped server up and switches its pack version (maintenance.Service).
type Maintenance interface {
	ChangeModpack(ctx context.Context, id string, ref domain.ModpackRef, game servers.VersionInput, actor servers.Actor) (servers.View, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}

const (
	searchMaxLen = 100
	searchMaxOff = 9_900 // CurseForge refuses index + page size beyond 10 000
	iconTTL      = time.Hour
	iconCacheMax = 1024
)

type Service struct {
	modrinth    Modrinth
	curseforge  CurseForge
	settings    Settings
	servers     Servers
	maintenance Maintenance
	audit       AuditRecorder
	now         func() time.Time

	mu    sync.Mutex
	icons map[string]iconEntry // "<source>/<project ID>" → icon URL
}

type iconEntry struct {
	url string
	at  time.Time
}

func NewService(m Modrinth, cf CurseForge, st Settings, srv Servers, mt Maintenance, audit AuditRecorder) *Service {
	return &Service{modrinth: m, curseforge: cf, settings: st, servers: srv, maintenance: mt, audit: audit,
		now: time.Now, icons: map[string]iconEntry{}}
}

// Search finds modpacks of source that run on a server.
func (s *Service) Search(ctx context.Context, source, query string, offset int) (domain.ModSearch, error) {
	query = strings.TrimSpace(query)
	switch {
	case utf8.RuneCountInString(query) > searchMaxLen:
		return domain.ModSearch{}, &domain.ValidationError{Field: "q", Message: fmt.Sprintf("must be at most %d characters", searchMaxLen)}
	case offset < 0 || offset > searchMaxOff:
		return domain.ModSearch{}, &domain.ValidationError{Field: "offset", Message: fmt.Sprintf("must be between 0 and %d", searchMaxOff)}
	}
	if err := domain.ValidateModpackSource(source); err != nil {
		return domain.ModSearch{}, err
	}
	var res domain.ModSearch
	var err error
	if source == domain.ModpackSourceModrinth {
		res, err = s.modrinth.SearchModpacks(ctx, query, offset)
	} else {
		var key string
		if key, err = s.key(ctx); err == nil {
			res, err = s.curseforge.SearchModpacks(ctx, key, query, offset)
		}
	}
	if err != nil {
		return domain.ModSearch{}, err
	}
	for _, p := range res.Hits {
		s.rememberIcon(source, p)
	}
	return res, nil
}

// Versions lists the versions of a modpack that the panel can run, newest first.
func (s *Service) Versions(ctx context.Context, source, projectID string) ([]domain.ModpackVersion, error) {
	if err := domain.ValidateModpackID(source, "project_id", projectID); err != nil {
		return nil, err
	}
	if source == domain.ModpackSourceCurseForge {
		key, err := s.key(ctx)
		if err != nil {
			return nil, err
		}
		return s.curseforge.Versions(ctx, key, projectID)
	}
	list, err := s.modrinth.ProjectVersions(ctx, projectID, "", "")
	if err != nil {
		return nil, err
	}
	out := []domain.ModpackVersion{}
	for _, v := range list {
		if pv, ok := modrinthVersion(v); ok {
			out = append(out, pv)
		}
	}
	domain.SortModpackVersions(out)
	return out, nil
}

// Resolve reads a modpack version from its catalog: the reference a server stores and the game
// (loader and Minecraft version) it runs. A version of another project is domain.ErrNotFound;
// one the panel cannot run is domain.ErrModpackUnsupported.
func (s *Service) Resolve(ctx context.Context, source, projectID, versionID string) (domain.ModpackRef, servers.VersionInput, error) {
	for _, err := range []error{
		domain.ValidateModpackID(source, "project_id", projectID),
		domain.ValidateModpackID(source, "version_id", versionID),
	} {
		if err != nil {
			return domain.ModpackRef{}, servers.VersionInput{}, err
		}
	}
	var p domain.ModProject
	var v domain.ModpackVersion
	if source == domain.ModpackSourceModrinth {
		ps, err := s.modrinth.Projects(ctx, []string{projectID})
		if err != nil {
			return domain.ModpackRef{}, servers.VersionInput{}, err
		}
		var found bool
		if p, found = ps[projectID]; !found {
			return domain.ModpackRef{}, servers.VersionInput{}, domain.ErrNotFound
		}
		mv, err := s.modrinth.Version(ctx, versionID)
		if err != nil {
			return domain.ModpackRef{}, servers.VersionInput{}, err
		}
		if mv.ProjectID != projectID {
			return domain.ModpackRef{}, servers.VersionInput{}, domain.ErrNotFound
		}
		if v, found = modrinthVersion(mv); !found {
			return domain.ModpackRef{}, servers.VersionInput{}, domain.ErrModpackUnsupported
		}
	} else {
		key, err := s.key(ctx)
		if err != nil {
			return domain.ModpackRef{}, servers.VersionInput{}, err
		}
		if p, err = s.curseforge.Modpack(ctx, key, projectID); err != nil {
			return domain.ModpackRef{}, servers.VersionInput{}, err
		}
		if v, err = s.curseforge.Version(ctx, key, projectID, versionID); err != nil {
			return domain.ModpackRef{}, servers.VersionInput{}, err
		}
	}
	ref := domain.ModpackRef{
		Source: source, ProjectID: projectID, Slug: p.Slug, VersionID: v.ID,
		Name: clip(p.Title, 100), VersionName: clip(v.Name, 100),
	}
	return ref, servers.VersionInput{Loader: v.Loader, Version: v.GameVersion}, nil
}

// ServerVersions lists the versions of a server's modpack. A server without a catalog
// modpack is domain.ErrNotFound.
func (s *Service) ServerVersions(ctx context.Context, id string) (domain.ModpackRef, []domain.ModpackVersion, error) {
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return domain.ModpackRef{}, nil, err
	}
	if !srv.Modpack.Set() {
		return domain.ModpackRef{}, nil, domain.ErrNotFound
	}
	list, err := s.Versions(ctx, srv.Modpack.Source, srv.Modpack.ProjectID)
	return srv.Modpack, list, err
}

// Update backs a stopped server up and makes it run another version of its modpack. The
// world is kept.
func (s *Service) Update(ctx context.Context, id, versionID string, actor servers.Actor) (servers.View, error) {
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return servers.View{}, err
	}
	if !srv.Modpack.Set() {
		return servers.View{}, domain.ErrNotFound
	}
	if versionID == srv.Modpack.VersionID {
		return servers.View{}, &domain.ValidationError{Field: "version_id", Message: "is the version the server already runs"}
	}
	ref, game, err := s.Resolve(ctx, srv.Modpack.Source, srv.Modpack.ProjectID, versionID)
	if err != nil {
		return servers.View{}, err
	}
	return s.maintenance.ChangeModpack(ctx, id, ref, game, actor)
}

// CurseForgeKeyStatus reports whether a CurseForge API key is stored and, when one is, its
// last four characters so the user can recognize it.
func (s *Service) CurseForgeKeyStatus(ctx context.Context) (configured bool, hint string, err error) {
	key, err := s.settings.CurseForgeKey(ctx)
	if err != nil || key == "" {
		return false, "", err
	}
	if len(key) > 4 {
		hint = key[len(key)-4:]
	}
	return true, hint, nil
}

// SetCurseForgeKey stores a CurseForge API key after CurseForge accepts it. Existing servers
// get the new key when their container is next recreated (a pack update).
func (s *Service) SetCurseForgeKey(ctx context.Context, key string, actor servers.Actor) error {
	if err := domain.ValidateCurseForgeKey(key); err != nil {
		return err
	}
	err := s.curseforge.Check(ctx, key)
	if err == nil {
		err = s.settings.SetCurseForgeKey(ctx, key)
	}
	s.record(ctx, actor, "set", err)
	return err
}

// RemoveCurseForgeKey forgets the CurseForge API key.
func (s *Service) RemoveCurseForgeKey(ctx context.Context, actor servers.Actor) error {
	err := s.settings.SetCurseForgeKey(ctx, "")
	s.record(ctx, actor, "removed", err)
	return err
}

// Icon returns a modpack's icon and its content type. A pack without one is
// domain.ErrNotFound.
func (s *Service) Icon(ctx context.Context, source, projectID string) ([]byte, string, error) {
	if err := domain.ValidateModpackID(source, "project_id", projectID); err != nil {
		return nil, "", err
	}
	cacheKey := source + "/" + projectID
	s.mu.Lock()
	e, ok := s.icons[cacheKey]
	s.mu.Unlock()
	if !ok || s.now().Sub(e.at) >= iconTTL {
		p, err := s.project(ctx, source, projectID)
		if err != nil {
			return nil, "", err
		}
		e = s.rememberIcon(source, p)
	}
	if e.url == "" {
		return nil, "", domain.ErrNotFound
	}
	if source == domain.ModpackSourceModrinth {
		return s.modrinth.Icon(ctx, e.url)
	}
	return s.curseforge.Icon(ctx, e.url)
}

func (s *Service) project(ctx context.Context, source, projectID string) (domain.ModProject, error) {
	if source == domain.ModpackSourceCurseForge {
		key, err := s.key(ctx)
		if err != nil {
			return domain.ModProject{}, err
		}
		return s.curseforge.Modpack(ctx, key, projectID)
	}
	ps, err := s.modrinth.Projects(ctx, []string{projectID})
	if err != nil {
		return domain.ModProject{}, err
	}
	p, ok := ps[projectID]
	if !ok {
		return domain.ModProject{}, domain.ErrNotFound
	}
	return p, nil
}

func (s *Service) rememberIcon(source string, p domain.ModProject) iconEntry {
	e := iconEntry{url: p.IconURL, at: s.now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.icons) >= iconCacheMax {
		clear(s.icons)
	}
	s.icons[source+"/"+p.ID] = e
	return e
}

// key returns the stored CurseForge API key, or domain.ErrCurseForgeNotConfigured.
func (s *Service) key(ctx context.Context) (string, error) {
	key, err := s.settings.CurseForgeKey(ctx)
	switch {
	case err != nil:
		return "", err
	case key == "":
		return "", domain.ErrCurseForgeNotConfigured
	}
	return key, nil
}

func (s *Service) record(ctx context.Context, actor servers.Actor, target string, err error) {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return // nothing was attempted
	}
	_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
		CreatedAt: s.now().UTC(), UserID: actor.UserID, Action: domain.AuditCurseForgeKey,
		Target: target, IP: actor.IP, Success: err == nil,
	})
}

// modrinthVersion keeps a Modrinth modpack version whose loader and Minecraft version the
// panel can run.
func modrinthVersion(v domain.ModVersion) (domain.ModpackVersion, bool) {
	loader, mc, ok := domain.ModpackGame(append(append([]string{}, v.Loaders...), v.GameVersions...))
	if !ok {
		return domain.ModpackVersion{}, false
	}
	return domain.ModpackVersion{ID: v.ID, Name: v.Name, Type: v.Type, Published: v.Published, Loader: loader, GameVersion: mc}, true
}

// clip shortens catalog text stored with a server.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
