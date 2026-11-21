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

// minecraftEnv configures the game image (itzg/minecraft-server) for x. The heap follows the
// container memory limit (MaxRAMPercentage), so changing the limit takes effect on the next
// start without recreating the container. The console uses stdin, so RCON stays off.
// Modded servers pin their loader build: without it the image would pick the newest one on
// every start and could upgrade the loader under the installed mods.
func minecraftEnv(x domain.Server) []string {
	env := []string{
		"EULA=TRUE", // only created after the user accepted the EULA
		"TYPE=" + strings.ToUpper(x.Loader),
		"VERSION=" + x.Version,
		"MEMORY=",
		"JVM_XX_OPTS=-XX:MaxRAMPercentage=75",
		"ENABLE_RCON=false",
	}
	if name, ok := loaderVersionEnv[x.Loader]; ok {
		env = append(env, name+"="+x.LoaderVersion)
	}
	return env
}
