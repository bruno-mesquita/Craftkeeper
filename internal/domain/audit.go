package domain

import "time"

// Audit actions. These strings are persisted and returned by the API; treat them as a contract.
const (
	AuditSetupCompleted  = "auth.setup"
	AuditLogin           = "auth.login"
	AuditLogout          = "auth.logout"
	AuditPasswordChanged = "auth.password_change"

	AuditServerCreate     = "server.create"
	AuditServerUpdate     = "server.update"
	AuditServerStart      = "server.start"
	AuditServerStop       = "server.stop"
	AuditServerRestart    = "server.restart"
	AuditServerKill       = "server.kill"
	AuditServerDelete     = "server.delete"
	AuditContainerDelete  = "container.delete"
	AuditModpackConfigure = "modpack.configure"
	AuditModpackInstall   = "modpack.install"
	// AuditConsoleCommand is a command typed in the console; the target is the command,
	// truncated. Commands the panel sends on its own (backups, player lists) are not recorded
	// as console commands: the action that sent them is.
	AuditConsoleCommand = "server.console_command"

	// AuditServerCrashLoop is recorded without a user when the panel stops a server that
	// kept crashing; the target is "<server name>: <n> restarts".
	AuditServerCrashLoop = "server.crash_loop"

	// AuditServerVersionChange targets "<server name>: <old version> → <new version>".
	AuditServerVersionChange = "server.version_change"
	// AuditServerMigrate is no longer recorded: it marks the one-time recreation of containers
	// created before the rename to Craftkeeper (D69). Kept so those audit rows stay filterable.
	AuditServerMigrate = "server.migrate"
	// World actions target "<server name>: <world directory>".
	AuditWorldReset  = "world.reset"
	AuditWorldImport = "world.import"

	// File actions target "<server name>: <path>".
	AuditFileUpload  = "file.upload"
	AuditFileWrite   = "file.write"
	AuditFileMkdir   = "file.mkdir"
	AuditFileRename  = "file.rename"
	AuditFileDelete  = "file.delete"
	AuditFileExtract = "file.extract"

	// Backup actions target "<server name>: <backup id>" (schedule: the server name).
	// Scheduled backups are recorded without a user.
	AuditBackupCreate   = "backup.create"
	AuditBackupRestore  = "backup.restore"
	AuditBackupDelete   = "backup.delete"
	AuditBackupSchedule = "backup.schedule_update"

	// Player list actions target "<server name>: <player name or IP>" (toggle: "on" or "off").
	AuditWhitelistToggle = "whitelist.toggle"
	AuditWhitelistAdd    = "whitelist.add"
	AuditWhitelistRemove = "whitelist.remove"
	AuditPlayerBan       = "player.ban"
	AuditPlayerPardon    = "player.pardon"
	AuditIPBan           = "ip.ban"
	AuditIPPardon        = "ip.pardon"
	AuditPlayerOp        = "player.op"
	AuditPlayerDeop      = "player.deop"

	// Mod actions target "<server name>: <file>" ("<old file> → <new file>" for an update).
	AuditModInstall = "mod.install"
	AuditModUpdate  = "mod.update"
	AuditModRemove  = "mod.remove"
	AuditModEnable  = "mod.enable"
	AuditModDisable = "mod.disable"

	// AuditModpackUpdate targets "<server name>: <pack> <old version> → <new version>".
	AuditModpackUpdate = "modpack.update"
	// AuditCurseForgeKey records saving ("set") or removing ("removed") the CurseForge API key.
	AuditCurseForgeKey = "settings.curseforge_key"
)

// AuditActions lists every action, in display order. Filters only accept these.
var AuditActions = []string{
	AuditSetupCompleted, AuditLogin, AuditLogout, AuditPasswordChanged,
	AuditServerCreate, AuditServerUpdate, AuditServerStart, AuditServerStop, AuditServerRestart,
	AuditServerKill, AuditServerDelete, AuditServerCrashLoop, AuditServerVersionChange, AuditServerMigrate, AuditConsoleCommand,
	AuditWorldReset, AuditWorldImport,
	AuditContainerDelete, AuditModpackConfigure, AuditModpackInstall, AuditModpackUpdate, AuditCurseForgeKey,
	AuditFileUpload, AuditFileWrite, AuditFileMkdir, AuditFileRename, AuditFileDelete, AuditFileExtract,
	AuditBackupCreate, AuditBackupRestore, AuditBackupDelete, AuditBackupSchedule,
	AuditWhitelistToggle, AuditWhitelistAdd, AuditWhitelistRemove,
	AuditPlayerBan, AuditPlayerPardon, AuditIPBan, AuditIPPardon, AuditPlayerOp, AuditPlayerDeop,
	AuditModInstall, AuditModUpdate, AuditModRemove, AuditModEnable, AuditModDisable,
}

type AuditEvent struct {
	ID        int64
	CreatedAt time.Time
	UserID    string // empty when the actor is unknown (e.g. failed login) or the panel itself
	ServerID  string // empty for events not about one server
	Action    string
	Target    string
	IP        string
	Success   bool

	// Filled when reading: the user's name and the server's current name, empty when there
	// is none or it no longer exists.
	Username   string
	ServerName string
}

// AuditFilter selects audit events, newest first. Zero fields do not filter.
type AuditFilter struct {
	ServerID   string
	Action     string
	FailedOnly bool
	BeforeID   int64 // only events with a smaller id (keyset pagination)
	Limit      int
}
