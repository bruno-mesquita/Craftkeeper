// Package mods installs, updates and removes a server's mods from Modrinth. Nothing is stored
// in the database: the jars in the mods directory are the state, and Modrinth identifies each
// one by its SHA-512, so mods uploaded by hand and restored backups are handled the same way.
// Mods may change while the server runs; the game reads them on its next start.
package mods

import (
	"cmp"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Catalog reads Modrinth (modrinth.Client). Download's reader fails instead of reaching EOF
// when the file does not match its SHA-512.
type Catalog interface {
	Search(ctx context.Context, query, loader, mc string, offset int) (domain.ModSearch, error)
	Projects(ctx context.Context, ids []string) (map[string]domain.ModProject, error)
	ProjectVersions(ctx context.Context, projectID, loader, mc string) ([]domain.ModVersion, error)
	Version(ctx context.Context, id string) (domain.ModVersion, error)
	VersionsByHash(ctx context.Context, hashes []string) (map[string]domain.ModVersion, error)
	LatestByHash(ctx context.Context, hashes []string, loader, mc string) (map[string]domain.ModVersion, error)
	Download(ctx context.Context, f domain.ModFile) (io.ReadCloser, error)
	Icon(ctx context.Context, url string) ([]byte, string, error)
}

// Files reaches a server's directory (filesystem.ServerFiles).
type Files interface {
	List(id, dir string) (domain.FileListing, error)
	Stat(id, p string) (domain.FileEntry, error)
	OpenFile(id, p string) (*os.File, int64, error)
	Save(id, dir, n string, src io.Reader, overwrite bool) (domain.FileEntry, error)
	Mkdir(id, p string) error
	Rename(id, from, to string) error
	Remove(id, p string) error
	FreeBytes(id string) (int64, error)
}

// Servers resolves a server (servers.Service).
type Servers interface {
	Get(ctx context.Context, id string) (servers.View, error)
}

// Backups makes the backup that protects an update (backups.Service).
type Backups interface {
	Protect(ctx context.Context, serverID string, actor servers.Actor, change func(ctx context.Context) error) error
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}

const (
	// reserveBytes is the free space downloads must leave on the disk (as for backups).
	reserveBytes = 2 << 30
	searchMaxLen = 100
	searchMaxOff = 10_000
	iconTTL      = time.Hour
	iconCacheMax = 1024
)

type Service struct {
	catalog Catalog
	files   Files
	servers Servers
	backups Backups
	audit   AuditRecorder
	now     func() time.Time
	reserve int64

	mu     sync.Mutex
	busy   map[string]bool      // server ID → a mod operation is running
	hashes map[string]hashEntry // "<server id>/<file>" → SHA-512 of the file
	icons  map[string]iconEntry // project ID → icon URL
}

type hashEntry struct {
	size int64
	mod  time.Time
	sum  string
}

type iconEntry struct {
	url string
	at  time.Time
}

func NewService(c Catalog, f Files, s Servers, b Backups, audit AuditRecorder) *Service {
	return &Service{
		catalog: c, files: f, servers: s, backups: b, audit: audit, now: time.Now, reserve: reserveBytes,
		busy: map[string]bool{}, hashes: map[string]hashEntry{}, icons: map[string]iconEntry{},
	}
}

// List is a server's installed mods, by file name.
type List struct {
	Mods []domain.InstalledMod
	// CatalogUnavailable is set when Modrinth could not be asked: the files are listed
	// without projects, versions or updates.
	CatalogUnavailable bool
}

// Change is one updated mod file.
type Change struct{ From, To string }

// Result lists the files an install or update wrote.
type Result struct {
	Installed []string // new mods, dependencies included
	Updated   []Change
}

// List returns the jars in the mods directory, identified by Modrinth when possible, with
// the newest compatible version of each.
func (s *Service) List(ctx context.Context, id string) (List, error) {
	srv, err := s.server(ctx, id)
	if err != nil {
		return List{}, err
	}
	mods, hashes, err := s.scan(id)
	if err != nil {
		return List{}, err
	}
	err = s.identify(ctx, srv, mods, hashes, true)
	switch {
	case errors.Is(err, domain.ErrModrinthUnavailable):
		return List{Mods: mods, CatalogUnavailable: true}, nil
	case err != nil:
		return List{}, err
	}
	return List{Mods: mods}, nil
}

// Search finds mods that run on the server's loader and Minecraft version.
func (s *Service) Search(ctx context.Context, id, query string, offset int) (domain.ModSearch, error) {
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) > searchMaxLen {
		return domain.ModSearch{}, &domain.ValidationError{Field: "q", Message: fmt.Sprintf("must be at most %d characters", searchMaxLen)}
	}
	if offset < 0 || offset > searchMaxOff {
		return domain.ModSearch{}, &domain.ValidationError{Field: "offset", Message: fmt.Sprintf("must be between 0 and %d", searchMaxOff)}
	}
	srv, err := s.server(ctx, id)
	if err != nil {
		return domain.ModSearch{}, err
	}
	return s.catalog.Search(ctx, query, srv.Loader, srv.Version, offset)
}

// Versions lists a project's versions that run on the server, newest first.
func (s *Service) Versions(ctx context.Context, id, projectID string) ([]domain.ModVersion, error) {
	if err := domain.ValidateModrinthID("project_id", projectID); err != nil {
		return nil, err
	}
	srv, err := s.server(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.versions(ctx, srv, projectID)
}

// Install downloads a mod (versionID, or the newest compatible version when empty) and the
// required dependencies that are not installed yet. It is all or nothing: a failed download
// removes the files this call wrote.
func (s *Service) Install(ctx context.Context, id, projectID, versionID string, actor servers.Actor) (Result, error) {
	if err := domain.ValidateModrinthID("project_id", projectID); err != nil {
		return Result{}, err
	}
	if versionID != "" {
		if err := domain.ValidateModrinthID("version_id", versionID); err != nil {
			return Result{}, err
		}
	}
	release, err := s.lock(id)
	if err != nil {
		return Result{}, err
	}
	defer release()
	srv, err := s.server(ctx, id)
	if err != nil {
		return Result{}, err
	}
	mods, hashes, err := s.scan(id)
	if err != nil {
		return Result{}, err
	}
	if err := s.identify(ctx, srv, mods, hashes, false); err != nil {
		return Result{}, err
	}
	if installedProjects(mods)[projectID] {
		return Result{}, &domain.ValidationError{Field: "project_id", Message: "is already installed; update it instead"}
	}
	root, err := s.pick(ctx, srv, projectID, versionID)
	if err != nil {
		return Result{}, err
	}
	plan, err := s.resolve(ctx, srv, []domain.ModVersion{root}, mods)
	if err != nil {
		return Result{}, err
	}
	items := make([]item, 0, len(plan))
	for _, v := range plan {
		name, err := domain.SafeModFileName(v.File.Name)
		if err != nil {
			return Result{}, err
		}
		items = append(items, item{version: v, name: name})
	}
	written, err := s.write(ctx, id, items)
	if err != nil {
		s.record(ctx, actor, domain.AuditModInstall, srv, items[0].name, err)
		return Result{}, err
	}
	for _, n := range written {
		s.record(ctx, actor, domain.AuditModInstall, srv, n, nil)
	}
	return Result{Installed: written}, nil
}

// Update replaces mods with their newest compatible version, after a full backup of the
// server when backup is set. files names the mods to update; empty updates every mod that has
// an update. A disabled mod stays disabled. New required dependencies are installed too.
func (s *Service) Update(ctx context.Context, id string, files []string, backup bool, actor servers.Actor) (Result, error) {
	for _, f := range files {
		if _, err := domain.ValidateModFile(f); err != nil {
			return Result{}, err
		}
	}
	release, err := s.lock(id)
	if err != nil {
		return Result{}, err
	}
	defer release()
	srv, err := s.server(ctx, id)
	if err != nil {
		return Result{}, err
	}
	mods, hashes, err := s.scan(id)
	if err != nil {
		return Result{}, err
	}
	if err := s.identify(ctx, srv, mods, hashes, true); err != nil {
		return Result{}, err
	}
	targets, err := updateTargets(mods, files)
	if err != nil || len(targets) == 0 {
		return Result{}, err
	}
	roots := make([]domain.ModVersion, len(targets))
	for i, t := range targets {
		roots[i] = *t.Update
	}
	plan, err := s.resolve(ctx, srv, roots, mods)
	if err != nil {
		return Result{}, err
	}
	items := make([]item, 0, len(plan))
	for i, v := range plan {
		name, err := domain.SafeModFileName(v.File.Name)
		if err != nil {
			return Result{}, err
		}
		it := item{version: v, name: name}
		if i < len(targets) { // an update: keep the mod's state; the same name replaces the file
			if !targets[i].Enabled {
				it.name += domain.DisabledModSuffix
			}
			it.overwrite = it.name == targets[i].File
		}
		items = append(items, it)
	}
	var res Result
	apply := func(ctx context.Context) error {
		if _, err := s.write(ctx, id, items); err != nil {
			return err
		}
		for i, t := range targets {
			if items[i].name != t.File {
				if err := s.files.Remove(id, domain.ModsDir+"/"+t.File); err != nil && !errors.Is(err, domain.ErrNotFound) {
					return fmt.Errorf("removing %s after its update: %w", t.File, err)
				}
			}
			res.Updated = append(res.Updated, Change{From: t.File, To: items[i].name})
		}
		for _, it := range items[len(targets):] {
			res.Installed = append(res.Installed, it.name)
		}
		return nil
	}
	if backup {
		err = s.backups.Protect(ctx, id, actor, apply)
	} else {
		err = apply(ctx)
	}
	if err != nil {
		s.record(ctx, actor, domain.AuditModUpdate, srv, targets[0].File, err)
		return Result{}, err
	}
	for _, c := range res.Updated {
		s.record(ctx, actor, domain.AuditModUpdate, srv, c.From+" → "+c.To, nil)
	}
	for _, n := range res.Installed {
		s.record(ctx, actor, domain.AuditModInstall, srv, n, nil)
	}
	return res, nil
}

// SetEnabled enables or disables a mod by renaming it to or from "<name>.disabled".
// It returns the mod's new file name.
func (s *Service) SetEnabled(ctx context.Context, id, file string, enabled bool, actor servers.Actor) (string, error) {
	current, err := domain.ValidateModFile(file)
	if err != nil {
		return "", err
	}
	release, err := s.lock(id)
	if err != nil {
		return "", err
	}
	defer release()
	srv, err := s.server(ctx, id)
	if err != nil {
		return "", err
	}
	if err := s.statMod(id, file); err != nil {
		return "", err
	}
	if current == enabled {
		return file, nil
	}
	to, action := file+domain.DisabledModSuffix, domain.AuditModDisable
	if enabled {
		to, action = strings.TrimSuffix(file, domain.DisabledModSuffix), domain.AuditModEnable
	}
	err = s.files.Rename(id, domain.ModsDir+"/"+file, domain.ModsDir+"/"+to)
	s.record(ctx, actor, action, srv, file, err)
	if err != nil {
		return "", err
	}
	return to, nil
}

// Remove deletes a mod file.
func (s *Service) Remove(ctx context.Context, id, file string, actor servers.Actor) error {
	if _, err := domain.ValidateModFile(file); err != nil {
		return err
	}
	release, err := s.lock(id)
	if err != nil {
		return err
	}
	defer release()
	srv, err := s.server(ctx, id)
	if err != nil {
		return err
	}
	if err := s.statMod(id, file); err != nil {
		return err
	}
	err = s.files.Remove(id, domain.ModsDir+"/"+file)
	s.record(ctx, actor, domain.AuditModRemove, srv, file, err)
	return err
}

// Icon returns a project's icon and its content type. A project without one is
// domain.ErrNotFound.
func (s *Service) Icon(ctx context.Context, projectID string) ([]byte, string, error) {
	if err := domain.ValidateModrinthID("project_id", projectID); err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	e, ok := s.icons[projectID]
	s.mu.Unlock()
	if !ok || s.now().Sub(e.at) >= iconTTL {
		ps, err := s.catalog.Projects(ctx, []string{projectID})
		if err != nil {
			return nil, "", err
		}
		p, found := ps[projectID]
		if !found {
			return nil, "", domain.ErrNotFound
		}
		e = iconEntry{url: p.IconURL, at: s.now()}
		s.mu.Lock()
		if len(s.icons) >= iconCacheMax {
			clear(s.icons)
		}
		s.icons[projectID] = e
		s.mu.Unlock()
	}
	if e.url == "" {
		return nil, "", domain.ErrNotFound
	}
	return s.catalog.Icon(ctx, e.url)
}

// server resolves a server whose mods the panel manages: built, with a mod loader, and not
// running a modpack (whose scripts own the mods).
func (s *Service) server(ctx context.Context, id string) (servers.View, error) {
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return servers.View{}, err
	}
	switch {
	case srv.Status == domain.StatusCreating || srv.Status == domain.StatusCreateFailed:
		return servers.View{}, domain.ErrServerBusy
	case !domain.Modded(srv.Loader):
		return servers.View{}, domain.ErrModsUnsupported
	}
	if _, err := s.files.Stat(id, domain.ModpackConfigFile); err == nil {
		return servers.View{}, domain.ErrModpackServer
	} else if !errors.Is(err, domain.ErrNotFound) {
		return servers.View{}, err
	}
	return srv, nil
}

// lock allows one mod operation per server at a time.
func (s *Service) lock(id string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] {
		return nil, domain.ErrServerBusy
	}
	s.busy[id] = true
	return func() {
		s.mu.Lock()
		delete(s.busy, id)
		s.mu.Unlock()
	}, nil
}

func (s *Service) statMod(id, file string) error {
	e, err := s.files.Stat(id, domain.ModsDir+"/"+file)
	if err != nil {
		return err
	}
	if e.Type != domain.FileTypeFile {
		return fmt.Errorf("%w: %s is not a regular file", domain.ErrNotFound, file)
	}
	return nil
}

// scan lists the mod jars and their SHA-512 (index-aligned). A missing mods directory has none.
func (s *Service) scan(id string) ([]domain.InstalledMod, []string, error) {
	l, err := s.files.List(id, domain.ModsDir)
	if errors.Is(err, domain.ErrNotFound) {
		return []domain.InstalledMod{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	mods := []domain.InstalledMod{}
	var hashes []string
	seen := map[string]bool{}
	for _, e := range l.Entries {
		if e.Type != domain.FileTypeFile {
			continue
		}
		enabled, err := domain.ValidateModFile(e.Name)
		if err != nil {
			continue
		}
		sum, err := s.hash(id, e)
		if err != nil {
			return nil, nil, err
		}
		seen[id+"/"+e.Name] = true
		mods = append(mods, domain.InstalledMod{File: e.Name, Enabled: enabled, Size: e.Size, ModifiedAt: e.ModifiedAt})
		hashes = append(hashes, sum)
	}
	s.mu.Lock()
	for k := range s.hashes {
		if strings.HasPrefix(k, id+"/") && !seen[k] {
			delete(s.hashes, k)
		}
	}
	s.mu.Unlock()
	return mods, hashes, nil
}

// hash returns a file's SHA-512, reusing the last one while its size and mtime are unchanged.
func (s *Service) hash(id string, e domain.FileEntry) (string, error) {
	key := id + "/" + e.Name
	s.mu.Lock()
	c, ok := s.hashes[key]
	s.mu.Unlock()
	if ok && c.size == e.Size && c.mod.Equal(e.ModifiedAt) {
		return c.sum, nil
	}
	f, _, err := s.files.OpenFile(id, domain.ModsDir+"/"+e.Name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hashing %s: %w", e.Name, err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	s.mu.Lock()
	s.hashes[key] = hashEntry{size: e.Size, mod: e.ModifiedAt, sum: sum}
	s.mu.Unlock()
	return sum, nil
}

// identify fills in the Modrinth project and version of each mod, and with updates the newest
// compatible version when it is newer than the installed one.
func (s *Service) identify(ctx context.Context, srv servers.View, mods []domain.InstalledMod, hashes []string, updates bool) error {
	if len(hashes) == 0 {
		return nil
	}
	byHash, err := s.catalog.VersionsByHash(ctx, hashes)
	if err != nil {
		return err
	}
	var ids []string
	for _, v := range byHash {
		if !slices.Contains(ids, v.ProjectID) {
			ids = append(ids, v.ProjectID)
		}
	}
	projects, err := s.catalog.Projects(ctx, ids)
	if err != nil {
		return err
	}
	var latest map[string]domain.ModVersion
	if updates {
		if latest, err = s.catalog.LatestByHash(ctx, hashes, srv.Loader, srv.Version); err != nil {
			return err
		}
	}
	for i, h := range hashes {
		v, ok := byHash[h]
		if !ok {
			continue
		}
		mods[i].Version = &v
		if p, ok := projects[v.ProjectID]; ok {
			mods[i].Project = &p
		}
		if l, ok := latest[h]; ok && l.ProjectID == v.ProjectID && l.ID != v.ID && l.Published.After(v.Published) && l.Supports(srv.Loader, srv.Version) {
			mods[i].Update = &l
		}
	}
	return nil
}

func installedProjects(mods []domain.InstalledMod) map[string]bool {
	out := map[string]bool{}
	for _, m := range mods {
		if m.Version != nil {
			out[m.Version.ProjectID] = true
		}
	}
	return out
}

func updateTargets(mods []domain.InstalledMod, files []string) ([]domain.InstalledMod, error) {
	var out []domain.InstalledMod
	if len(files) == 0 {
		for _, m := range mods {
			if m.Update != nil {
				out = append(out, m)
			}
		}
		return out, nil
	}
	for _, f := range files {
		i := slices.IndexFunc(mods, func(m domain.InstalledMod) bool { return m.File == f })
		switch {
		case i < 0:
			return nil, &domain.ValidationError{Field: "files", Message: f + " is not installed"}
		case mods[i].Update == nil:
			return nil, &domain.ValidationError{Field: "files", Message: f + " has no update"}
		case slices.ContainsFunc(out, func(m domain.InstalledMod) bool { return m.File == f }):
			continue
		}
		out = append(out, mods[i])
	}
	return out, nil
}

// versions lists a project's versions for the server, newest first. An unknown project is a
// validation error.
func (s *Service) versions(ctx context.Context, srv servers.View, projectID string) ([]domain.ModVersion, error) {
	list, err := s.catalog.ProjectVersions(ctx, projectID, srv.Loader, srv.Version)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, &domain.ValidationError{Field: "project_id", Message: "does not exist on Modrinth"}
	}
	if err != nil {
		return nil, err
	}
	list = slices.DeleteFunc(list, func(v domain.ModVersion) bool { return !v.Supports(srv.Loader, srv.Version) })
	slices.SortStableFunc(list, func(a, b domain.ModVersion) int { return b.Published.Compare(a.Published) })
	return list, nil
}

// pick returns the requested version of a project, checked against the server, or the newest
// compatible release (any type when there is no release).
func (s *Service) pick(ctx context.Context, srv servers.View, projectID, versionID string) (domain.ModVersion, error) {
	if versionID != "" {
		v, err := s.catalog.Version(ctx, versionID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return domain.ModVersion{}, &domain.ValidationError{Field: "version_id", Message: "does not exist on Modrinth"}
		case err != nil:
			return domain.ModVersion{}, err
		case v.ProjectID != projectID:
			return domain.ModVersion{}, &domain.ValidationError{Field: "version_id", Message: "belongs to another project"}
		case !v.Supports(srv.Loader, srv.Version):
			return domain.ModVersion{}, &domain.ValidationError{Field: "version_id", Message: "does not run on this server's loader and Minecraft version"}
		}
		return v, nil
	}
	list, err := s.versions(ctx, srv, projectID)
	if err != nil {
		return domain.ModVersion{}, err
	}
	if len(list) == 0 {
		return domain.ModVersion{}, &domain.ValidationError{Field: "project_id", Message: "has no version for this server's loader and Minecraft version"}
	}
	if i := slices.IndexFunc(list, func(v domain.ModVersion) bool { return v.Type == domain.ModVersionRelease }); i >= 0 {
		return list[i], nil
	}
	return list[0], nil
}

// resolve returns roots followed by the required dependencies that are neither installed nor
// already planned, breadth first. A planned mod that is incompatible with an installed or
// planned one fails with domain.ErrModIncompatible.
func (s *Service) resolve(ctx context.Context, srv servers.View, roots []domain.ModVersion, mods []domain.InstalledMod) ([]domain.ModVersion, error) {
	have := installedProjects(mods)
	planned := map[string]bool{}
	for _, r := range roots {
		planned[r.ProjectID] = true
	}
	out := slices.Clone(roots)
	for i := 0; i < len(out); i++ {
		for _, d := range out[i].Dependencies {
			if d.Kind != domain.ModDependencyRequired && d.Kind != domain.ModDependencyIncompatible {
				continue
			}
			var dv domain.ModVersion
			pid := d.ProjectID
			if d.VersionID != "" {
				v, err := s.catalog.Version(ctx, d.VersionID)
				if err != nil && !errors.Is(err, domain.ErrNotFound) {
					return nil, err
				}
				if err == nil {
					dv, pid = v, cmp.Or(pid, v.ProjectID)
				}
			}
			if pid == "" {
				continue // Modrinth no longer knows the dependency
			}
			if d.Kind == domain.ModDependencyIncompatible {
				if have[pid] || planned[pid] {
					return nil, fmt.Errorf("%w: %s declares %s incompatible", domain.ErrModIncompatible, out[i].ProjectID, pid)
				}
				continue
			}
			if have[pid] || planned[pid] {
				continue
			}
			if dv.ID == "" || dv.ProjectID != pid || !dv.Supports(srv.Loader, srv.Version) {
				v, err := s.pick(ctx, srv, pid, "")
				var ve *domain.ValidationError
				if errors.As(err, &ve) {
					return nil, &domain.ValidationError{Field: "project_id", Message: "requires mod " + pid + ", which has no version for this server's loader and Minecraft version"}
				}
				if err != nil {
					return nil, err
				}
				dv = v
			}
			if len(out) >= domain.ModsPerOperation {
				return nil, &domain.ValidationError{Field: "project_id", Message: fmt.Sprintf("needs more than %d mods at once", domain.ModsPerOperation)}
			}
			planned[pid] = true
			out = append(out, dv)
		}
	}
	// Installed mods may declare a planned one incompatible too (those being replaced do not count).
	for _, m := range mods {
		if m.Version == nil || planned[m.Version.ProjectID] {
			continue
		}
		for _, d := range m.Version.Dependencies {
			if d.Kind == domain.ModDependencyIncompatible && d.ProjectID != "" && planned[d.ProjectID] {
				return nil, fmt.Errorf("%w: %s declares %s incompatible", domain.ErrModIncompatible, m.File, d.ProjectID)
			}
		}
	}
	return out, nil
}

// item is one file to download into the mods directory.
type item struct {
	version   domain.ModVersion
	name      string
	overwrite bool // replaces a file of the same name (an update that kept its name)
}

// write downloads items into the mods directory, verifying each, after checking that they fit
// on the disk. On failure the files written by this call are removed (overwritten ones cannot
// be restored; updates run under a backup). It returns the names written, in order.
func (s *Service) write(ctx context.Context, id string, items []item) (written []string, err error) {
	var total int64
	for _, it := range items {
		total += it.version.File.Size
	}
	free, err := s.files.FreeBytes(id)
	if err != nil {
		return nil, err
	}
	if free-s.reserve < total {
		return nil, fmt.Errorf("%w: %d bytes of mods need %d free with the reserve, %d available", domain.ErrInsufficientStorage, total, total+s.reserve, free)
	}
	if _, err := s.files.Stat(id, domain.ModsDir); errors.Is(err, domain.ErrNotFound) {
		if err := s.files.Mkdir(id, domain.ModsDir); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			for i, n := range written {
				if !items[i].overwrite {
					_ = s.files.Remove(id, domain.ModsDir+"/"+n)
				}
			}
			written = nil
		}
	}()
	for _, it := range items {
		if err := s.download(ctx, id, it); err != nil {
			return written, err
		}
		written = append(written, it.name)
	}
	return written, nil
}

func (s *Service) download(ctx context.Context, id string, it item) error {
	rc, err := s.catalog.Download(ctx, it.version.File)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = s.files.Save(id, domain.ModsDir, it.name, rc, it.overwrite)
	return err
}

func (s *Service) record(ctx context.Context, actor servers.Actor, action string, srv servers.View, target string, err error) {
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return // nothing was attempted
	}
	_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
		CreatedAt: s.now().UTC(), UserID: actor.UserID, ServerID: srv.ID, Action: action,
		Target: srv.Name + ": " + target, IP: actor.IP, Success: err == nil,
	})
}
