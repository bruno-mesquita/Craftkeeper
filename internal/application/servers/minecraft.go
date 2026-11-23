package servers

import (
	"strings"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// loaderVersionEnv names the variable that pins each loader's build in the game image.
var loaderVersionEnv = map[string]string{
	domain.LoaderFabric:   "FABRIC_LOADER_VERSION",
	domain.LoaderForge:    "FORGE_VERSION",
	domain.LoaderNeoForge: "NEOFORGE_VERSION",
}

// cfDownloadsDir is where a CurseForge pack's game image looks for the mods whose authors
// forbid downloads by third parties: the user uploads them there through the file manager.
const cfDownloadsDir = "/data/downloads"

// minecraftEnv configures the game image (itzg/minecraft-server) for x. The heap follows the
// container memory limit (MaxRAMPercentage), so changing the limit takes effect on the next
// start without recreating the container. The console uses stdin, so RCON stays off.
// Modded servers pin their loader build: without it the image would pick the newest one on
// every start and could upgrade the loader under the installed mods. A catalog modpack pins
// its version instead, and the image installs the pack (and its loader) on start; when the
// version changes it removes the previous version's files, never the world. cfKey is only
// used by CurseForge packs.
func minecraftEnv(x domain.Server, cfKey string) []string {
	env := []string{
		"EULA=TRUE", // only created after the user accepted the EULA
		"MEMORY=",
		"JVM_XX_OPTS=-XX:MaxRAMPercentage=75",
		"ENABLE_RCON=false",
	}
	switch x.Modpack.Source {
	case domain.ModpackSourceModrinth:
		return append(env, "TYPE=MODRINTH", "VERSION="+x.Version,
			"MODRINTH_MODPACK="+x.Modpack.ProjectID, "MODRINTH_VERSION="+x.Modpack.VersionID)
	case domain.ModpackSourceCurseForge:
		return append(env, "TYPE=AUTO_CURSEFORGE", "CF_API_KEY="+cfKey,
			"CF_SLUG="+x.Modpack.Slug, "CF_FILE_ID="+x.Modpack.VersionID, "CF_DOWNLOADS_REPO="+cfDownloadsDir)
	}
	env = append(env, "TYPE="+strings.ToUpper(x.Loader), "VERSION="+x.Version)
	if name, ok := loaderVersionEnv[x.Loader]; ok {
		env = append(env, name+"="+x.LoaderVersion)
	}
	return env
}
