// Package installers serves the game versions each loader supports and pins the exact
// versions a new server installs, so a loader never upgrades itself on a later start.
package installers

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/ttlcache"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// VersionSource reads the official version lists. Every value is already validated.
type VersionSource interface {
	Minecraft(ctx context.Context) ([]string, error) // releases, newest first
	Fabric(ctx context.Context) (map[string]domain.LoaderVersions, error)
	Forge(ctx context.Context) (map[string]domain.LoaderVersions, error)
	NeoForge(ctx context.Context) (map[string]domain.LoaderVersions, error)
}

type catalog = map[string]domain.LoaderVersions

// Service caches each source for ttl. A failed refresh is not cached.
type Service struct {
	minecraft *ttlcache.Cache[[]string]
	loaders   map[string]*ttlcache.Cache[catalog]
}

func NewService(src VersionSource, ttl time.Duration) *Service {
	return &Service{
		minecraft: ttlcache.New(ttl, src.Minecraft),
		loaders: map[string]*ttlcache.Cache[catalog]{
			domain.LoaderFabric:   ttlcache.New(ttl, src.Fabric),
			domain.LoaderForge:    ttlcache.New(ttl, src.Forge),
			domain.LoaderNeoForge: ttlcache.New(ttl, src.NeoForge),
		},
	}
}

// MinecraftVersions lists the game versions a loader can install, newest first. Vanilla
// starts with "LATEST". An unknown loader is domain.ErrNotFound.
func (s *Service) MinecraftVersions(ctx context.Context, loader string) ([]string, error) {
	if loader == domain.LoaderVanilla {
		releases, err := s.minecraft.Get(ctx)
		if err != nil {
			return nil, err
		}
		return append([]string{domain.MinecraftLatest}, releases...), nil
	}
	c, err := s.catalog(ctx, loader)
	if err != nil {
		return nil, err
	}
	return slices.SortedFunc(maps.Keys(c), func(a, b string) int { return domain.CompareVersions(b, a) }), nil
}

// LoaderVersions lists the builds of a loader for one game release. A vanilla, unknown or
// unsupported combination is domain.ErrNotFound.
func (s *Service) LoaderVersions(ctx context.Context, loader, mcVersion string) (domain.LoaderVersions, error) {
	c, err := s.catalog(ctx, loader)
	if err != nil {
		return domain.LoaderVersions{}, err
	}
	lv, ok := c[mcVersion]
	if !ok {
		return domain.LoaderVersions{}, domain.ErrNotFound
	}
	return lv, nil
}

// Resolve checks a server's loader and versions and returns the loader build to install:
// the requested one if it exists, the recommended one when none was requested, and "" for
// vanilla. Vanilla never needs the network: the game image resolves its version itself.
func (s *Service) Resolve(ctx context.Context, loader, mcVersion, loaderVersion string) (string, error) {
	if err := domain.ValidateLoader(loader); err != nil {
		return "", err
	}
	if !domain.Modded(loader) {
		if loaderVersion != "" {
			return "", &domain.ValidationError{Field: "loader_version", Message: "must be empty for vanilla"}
		}
		return "", domain.ValidateMinecraftVersion(mcVersion)
	}
	if !domain.IsMinecraftRelease(mcVersion) {
		return "", &domain.ValidationError{Field: "version", Message: `must be a release such as "1.21.1" for modded servers`}
	}
	if loaderVersion != "" && !domain.ValidLoaderVersion(loaderVersion) {
		return "", &domain.ValidationError{Field: "loader_version", Message: "is not a valid loader version"}
	}
	c, err := s.catalog(ctx, loader)
	if err != nil {
		return "", err
	}
	lv, ok := c[mcVersion]
	if !ok {
		return "", &domain.ValidationError{Field: "version", Message: "is not supported by this loader"}
	}
	if loaderVersion == "" {
		return lv.Recommended, nil
	}
	if !slices.Contains(lv.Versions, loaderVersion) {
		return "", &domain.ValidationError{Field: "loader_version", Message: "does not exist for this Minecraft version"}
	}
	return loaderVersion, nil
}

func (s *Service) catalog(ctx context.Context, loader string) (catalog, error) {
	cache, ok := s.loaders[loader]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return cache.Get(ctx)
}
