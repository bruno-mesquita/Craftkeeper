package servers

import (
	"strings"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// minecraftEnv configures the game image (itzg/minecraft-server) for x. The heap follows the
// container memory limit (MaxRAMPercentage), so changing the limit takes effect on the next
// start without recreating the container. RCON stays off until the console needs it.
func minecraftEnv(x domain.Server) []string {
	return []string{
		"EULA=TRUE", // only created after the user accepted the EULA
		"TYPE=" + strings.ToUpper(x.Loader),
		"VERSION=" + x.Version,
		"MEMORY=",
		"JVM_XX_OPTS=-XX:MaxRAMPercentage=75",
		"ENABLE_RCON=false",
	}
}
