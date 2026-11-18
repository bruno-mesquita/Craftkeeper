// Package players manages a Minecraft server's whitelist and ban lists. A running server
// owns those files and rewrites them, so changes go through its console; a stopped server's
// files are edited directly. Reads always come from the files.
package players

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/application/servers"
	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Files reads and writes text files of a server's directory (filesystem.ServerFiles).
type Files interface {
	ReadText(id, p string) (domain.FileText, error)
	WriteText(id, p, content, version string) (string, error)
}

// Servers resolves a server (servers.Service).
type Servers interface {
	Get(ctx context.Context, id string) (servers.View, error)
}

// Console sends a command to a running server (console.Service).
type Console interface {
	SendCommand(ctx context.Context, serverID, command string, actor servers.Actor) error
}

// Accounts looks up Minecraft Java accounts at Mojang (mojang.Profiles).
type Accounts interface {
	Lookup(ctx context.Context, name string) (domain.Player, error)
	Face(ctx context.Context, uuid string) ([]byte, error)
}

// Pinger asks a running game who is online (minecraft.Pinger).
type Pinger interface {
	Status(ctx context.Context, port int) (domain.OnlinePlayers, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, e domain.AuditEvent) error
}

type Service struct {
	files    Files
	servers  Servers
	console  Console
	accounts Accounts
	pinger   Pinger
	audit    AuditRecorder
	now      func() time.Time
}

func NewService(f Files, s Servers, c Console, accounts Accounts, pinger Pinger, audit AuditRecorder) *Service {
	return &Service{files: f, servers: s, console: c, accounts: accounts, pinger: pinger, audit: audit, now: time.Now}
}

// Online asks the running game who is connected. A game still loading its world does not
// answer yet: ErrGameUnreachable.
func (s *Service) Online(ctx context.Context, id string) (domain.OnlinePlayers, error) {
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return domain.OnlinePlayers{}, err
	}
	switch srv.Status {
	case domain.StatusRunning, domain.StatusStarting:
	case domain.StatusUnknown:
		return domain.OnlinePlayers{}, domain.ErrDockerUnavailable
	default:
		return domain.OnlinePlayers{}, domain.ErrServerNotRunning
	}
	o, err := s.pinger.Status(ctx, srv.Port)
	if err != nil {
		return domain.OnlinePlayers{}, fmt.Errorf("%w: %v", domain.ErrGameUnreachable, err)
	}
	return o, nil
}

// Lists is what the Players tab shows.
type Lists struct {
	WhitelistEnabled bool
	Whitelist        []domain.Player
	BannedPlayers    []domain.PlayerBan
	BannedIPs        []domain.IPBan
}

func (s *Service) List(ctx context.Context, id string) (Lists, error) {
	if _, err := s.servers.Get(ctx, id); err != nil {
		return Lists{}, err
	}
	var l Lists
	props, err := s.read(id, domain.PropertiesFile)
	if err != nil {
		return l, err
	}
	v, _ := domain.PropertyValue(props.Content, "white-list")
	l.WhitelistEnabled = strings.TrimSpace(v) == "true"
	if l.Whitelist, err = readList[domain.Player](s, id, domain.WhitelistFile); err != nil {
		return l, err
	}
	if l.BannedPlayers, err = readList[domain.PlayerBan](s, id, domain.BannedPlayersFile); err != nil {
		return l, err
	}
	l.BannedIPs, err = readList[domain.IPBan](s, id, domain.BannedIPsFile)
	return l, err
}

func (s *Service) SetWhitelistEnabled(ctx context.Context, id string, enabled bool, actor servers.Actor) error {
	state := "off"
	if enabled {
		state = "on"
	}
	return s.mutate(ctx, id, actor, domain.AuditWhitelistToggle, state, "whitelist "+state, func() error {
		return s.edit(id, domain.PropertiesFile, func(c string) (string, error) {
			c = domain.SetProperty(c, "white-list", boolText(enabled))
			return domain.SetProperty(c, "enforce-whitelist", boolText(enabled)), nil
		})
	})
}

func (s *Service) AddWhitelist(ctx context.Context, id, name string, actor servers.Actor) error {
	if err := domain.ValidatePlayerName(name); err != nil {
		return err
	}
	return s.mutate(ctx, id, actor, domain.AuditWhitelistAdd, name, "whitelist add "+name, func() error {
		player, err := s.resolve(ctx, id, name)
		if err != nil {
			return err
		}
		return editList(s, id, domain.WhitelistFile, func(l []domain.Player) []domain.Player {
			l = without(l, func(p domain.Player) bool { return strings.EqualFold(p.Name, name) || p.UUID == player.UUID })
			return append(l, player)
		})
	})
}

func (s *Service) RemoveWhitelist(ctx context.Context, id, name string, actor servers.Actor) error {
	if err := domain.ValidatePlayerName(name); err != nil {
		return err
	}
	return s.mutate(ctx, id, actor, domain.AuditWhitelistRemove, name, "whitelist remove "+name, func() error {
		return editList(s, id, domain.WhitelistFile, func(l []domain.Player) []domain.Player {
			return without(l, func(p domain.Player) bool { return strings.EqualFold(p.Name, name) })
		})
	})
}

func (s *Service) Ban(ctx context.Context, id, name, reason string, actor servers.Actor) error {
	if err := domain.ValidatePlayerName(name); err != nil {
		return err
	}
	if err := domain.ValidateBanReason(reason); err != nil {
		return err
	}
	return s.mutate(ctx, id, actor, domain.AuditPlayerBan, name, strings.TrimSpace("ban "+name+" "+reason), func() error {
		player, err := s.resolve(ctx, id, name)
		if err != nil {
			return err
		}
		return editList(s, id, domain.BannedPlayersFile, func(l []domain.PlayerBan) []domain.PlayerBan {
			l = without(l, func(b domain.PlayerBan) bool { return strings.EqualFold(b.Name, name) || b.UUID == player.UUID })
			return append(l, domain.PlayerBan{UUID: player.UUID, Name: player.Name, Created: domain.BanTimestamp(s.now()),
				Source: "Mesquita Panel", Expires: "forever", Reason: banReason(reason)})
		})
	})
}

func (s *Service) Pardon(ctx context.Context, id, name string, actor servers.Actor) error {
	if err := domain.ValidatePlayerName(name); err != nil {
		return err
	}
	return s.mutate(ctx, id, actor, domain.AuditPlayerPardon, name, "pardon "+name, func() error {
		return editList(s, id, domain.BannedPlayersFile, func(l []domain.PlayerBan) []domain.PlayerBan {
			return without(l, func(b domain.PlayerBan) bool { return strings.EqualFold(b.Name, name) })
		})
	})
}

func (s *Service) BanIP(ctx context.Context, id, ip, reason string, actor servers.Actor) error {
	ip, err := domain.NormalizeIP(ip)
	if err != nil {
		return err
	}
	if err := domain.ValidateBanReason(reason); err != nil {
		return err
	}
	return s.mutate(ctx, id, actor, domain.AuditIPBan, ip, strings.TrimSpace("ban-ip "+ip+" "+reason), func() error {
		return editList(s, id, domain.BannedIPsFile, func(l []domain.IPBan) []domain.IPBan {
			l = without(l, func(b domain.IPBan) bool { return b.IP == ip })
			return append(l, domain.IPBan{IP: ip, Created: domain.BanTimestamp(s.now()),
				Source: "Mesquita Panel", Expires: "forever", Reason: banReason(reason)})
		})
	})
}

func (s *Service) PardonIP(ctx context.Context, id, ip string, actor servers.Actor) error {
	ip, err := domain.NormalizeIP(ip)
	if err != nil {
		return err
	}
	return s.mutate(ctx, id, actor, domain.AuditIPPardon, ip, "pardon-ip "+ip, func() error {
		return editList(s, id, domain.BannedIPsFile, func(l []domain.IPBan) []domain.IPBan {
			return without(l, func(b domain.IPBan) bool { return b.IP == ip })
		})
	})
}

// mutate applies one change: through the console while the game runs, through the files
// while it is stopped. The audit entry names the player or IP; failures are recorded too.
func (s *Service) mutate(ctx context.Context, id string, actor servers.Actor, action, target, command string, viaFiles func() error) error {
	srv, err := s.servers.Get(ctx, id)
	if err != nil {
		return err
	}
	switch srv.Status {
	case domain.StatusRunning, domain.StatusStarting:
		err = s.console.SendCommand(ctx, id, command, actor)
	case domain.StatusStopped, domain.StatusMissing:
		err = viaFiles()
	case domain.StatusUnknown:
		return domain.ErrDockerUnavailable
	default:
		return domain.ErrServerBusy
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		_ = s.audit.Record(context.WithoutCancel(ctx), domain.AuditEvent{
			CreatedAt: s.now().UTC(), UserID: actor.UserID, Action: action,
			Target: srv.Name + ": " + target, IP: actor.IP, Success: err == nil,
		})
	}
	return err
}

// resolve is the entry to store for a player while the server is stopped: the computed
// UUID on an offline-mode server, otherwise the Mojang account (UUID and name spelling).
func (s *Service) resolve(ctx context.Context, id, name string) (domain.Player, error) {
	props, err := s.read(id, domain.PropertiesFile)
	if err != nil {
		return domain.Player{}, err
	}
	if v, ok := domain.PropertyValue(props.Content, "online-mode"); ok && strings.TrimSpace(v) == "false" {
		return domain.Player{UUID: domain.OfflineUUID(name), Name: name}, nil
	}
	p, err := s.accounts.Lookup(ctx, name)
	if errors.Is(err, domain.ErrPlayerNotFound) {
		return p, &domain.ValidationError{Field: "name", Message: "no Minecraft account has this name"}
	}
	return p, err
}

// Avatar is the PNG face of a player's skin. Offline-mode UUIDs have no account behind them.
func (s *Service) Avatar(ctx context.Context, uuid string) ([]byte, error) {
	uuid, err := domain.ParseUUID(uuid)
	if err != nil {
		return nil, err
	}
	if domain.IsOfflineUUID(uuid) {
		return nil, domain.ErrNotFound
	}
	return s.accounts.Face(ctx, uuid)
}

// read returns a file's text; a missing file reads as empty.
func (s *Service) read(id, p string) (domain.FileText, error) {
	ft, err := s.files.ReadText(id, p)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.FileText{}, nil
	}
	return ft, err
}

// edit rewrites a text file, refusing to clobber a change made since it was read.
func (s *Service) edit(id, p string, f func(string) (string, error)) error {
	ft, err := s.read(id, p)
	if err != nil {
		return err
	}
	out, err := f(ft.Content)
	if err != nil {
		return err
	}
	_, err = s.files.WriteText(id, p, out, ft.Version)
	return err
}

func readList[T any](s *Service, id, p string) ([]T, error) {
	ft, err := s.read(id, p)
	if err != nil {
		return nil, err
	}
	return domain.ParsePlayerList[T](p, ft.Content)
}

func editList[T any](s *Service, id, p string, f func([]T) []T) error {
	return s.edit(id, p, func(c string) (string, error) {
		l, err := domain.ParsePlayerList[T](p, c)
		if err != nil {
			return "", err
		}
		return domain.EncodePlayerList(f(l)), nil
	})
}

func without[T any](l []T, match func(T) bool) []T {
	out := l[:0:0]
	for _, e := range l {
		if !match(e) {
			out = append(out, e)
		}
	}
	return out
}

func banReason(r string) string {
	if r == "" {
		return "Banned by an operator."
	}
	return r
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
