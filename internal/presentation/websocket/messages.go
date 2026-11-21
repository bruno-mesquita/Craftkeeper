package websocket

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/console"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// ProtocolVersion is the only message version this server speaks.
const ProtocolVersion = 1

const (
	maxRequestIDLen = 64
	serverChannel   = "server:"
)

// Message types (docs/WEBSOCKET.md).
const (
	// Client to server.
	typeSubscribe      = "subscribe"
	typeUnsubscribe    = "unsubscribe"
	typeConsoleCommand = "console.command"

	// Server to client.
	typeSubscribed     = "subscribed"
	typeUnsubscribed   = "unsubscribed"
	typeConsoleHistory = "console.history"
	typeConsoleOutput  = "console.output"
	typeServerStatus   = "server.status"
	typeAck            = "ack"
	typeError          = "error"
)

// inbound is a client message. Unknown fields are ignored.
type inbound struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Channel string          `json:"channel"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

type outbound struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	Channel string `json:"channel,omitempty"`
	ID      string `json:"id,omitempty"`
	Payload any    `json:"payload"`
}

type empty struct{}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type linePayload struct {
	Time   string `json:"time,omitempty"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type historyPayload struct {
	Lines []linePayload `json:"lines"`
}

type statusPayload struct {
	Status string `json:"status"`
}

type commandPayload struct {
	Command string `json:"command"`
}

func encode(typ, channel, id string, payload any) []byte {
	b, err := json.Marshal(outbound{Version: ProtocolVersion, Type: typ, Channel: channel, ID: id, Payload: payload})
	if err != nil {
		panic(err) // only our own payload types are encoded
	}
	return b
}

func toLine(l domain.ConsoleLine) linePayload {
	p := linePayload{Stream: string(l.Stream), Data: l.Text}
	if !l.Time.IsZero() {
		p.Time = l.Time.UTC().Format(time.RFC3339Nano)
	}
	return p
}

func (c *client) sendError(channel, id, code, message string) {
	c.enqueue(encode(typeError, channel, id, errorPayload{Code: code, Message: message}))
}

// handle routes one client message. Errors answer with an error message; the connection stays
// open, except for session failures.
func (c *client) handle(data []byte) {
	var m inbound
	if err := json.Unmarshal(data, &m); err != nil {
		c.sendError("", "", "invalid_message", "message is not valid JSON")
		return
	}
	if len(m.ID) > maxRequestIDLen {
		c.sendError("", "", "invalid_message", "id is too long")
		return
	}
	if m.Version != ProtocolVersion {
		c.sendError(m.Channel, m.ID, "unsupported_version", "unsupported message version")
		return
	}
	switch m.Type {
	case typeSubscribe, typeUnsubscribe, typeConsoleCommand:
	default:
		c.sendError(m.Channel, m.ID, "unknown_type", "unknown message type")
		return
	}
	serverID, ok := strings.CutPrefix(m.Channel, serverChannel)
	if !ok || !domain.ValidServerID(serverID) {
		c.sendError(m.Channel, m.ID, "invalid_channel", "unknown channel")
		return
	}

	switch m.Type {
	case typeSubscribe:
		c.subscribe(m, serverID)
	case typeUnsubscribe:
		unsub, ok := c.subs[m.Channel]
		if !ok {
			c.sendError(m.Channel, m.ID, "not_subscribed", "not subscribed to this channel")
			return
		}
		unsub()
		delete(c.subs, m.Channel)
		c.enqueue(encode(typeUnsubscribed, m.Channel, m.ID, empty{}))
	case typeConsoleCommand:
		c.command(m, serverID)
	}
}

func (c *client) subscribe(m inbound, serverID string) {
	if _, ok := c.subs[m.Channel]; ok {
		c.enqueue(encode(typeSubscribed, m.Channel, m.ID, empty{}))
		return
	}
	if len(c.subs) >= maxSubscriptions {
		c.sendError(m.Channel, m.ID, "too_many_subscriptions", "too many subscriptions on this connection")
		return
	}
	channel, first := m.Channel, true
	// The console calls the sink synchronously with the history before Subscribe returns,
	// so "subscribed" is guaranteed to precede every event of the channel.
	sink := func(e console.Event) {
		if first {
			first = false
			c.enqueue(encode(typeSubscribed, channel, m.ID, empty{}))
		}
		switch e.Kind {
		case console.EventHistory:
			lines := make([]linePayload, len(e.Lines))
			for i, l := range e.Lines {
				lines[i] = toLine(l)
			}
			c.enqueue(encode(typeConsoleHistory, channel, "", historyPayload{Lines: lines}))
		case console.EventLine:
			c.enqueue(encode(typeConsoleOutput, channel, "", toLine(e.Line)))
		case console.EventStatus:
			c.enqueue(encode(typeServerStatus, channel, "", statusPayload{Status: string(e.Status)}))
		}
	}
	unsub, err := c.hub.console.Subscribe(c.ctx, serverID, sink)
	if err != nil {
		c.sendDomainError(m.Channel, m.ID, err)
		return
	}
	c.subs[channel] = unsub
}

func (c *client) command(m inbound, serverID string) {
	var p commandPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		c.sendError(m.Channel, m.ID, "invalid_message", "payload must be {\"command\": string}")
		return
	}
	// Commands change game state: never act on a session revoked since the last check.
	if !c.checkSession() {
		return
	}
	if err := c.hub.console.SendCommand(c.ctx, serverID, p.Command, c.actor); err != nil {
		c.sendDomainError(m.Channel, m.ID, err)
		return
	}
	c.enqueue(encode(typeAck, m.Channel, m.ID, empty{}))
}

// sendDomainError maps domain errors to protocol error codes. Daemon and database details
// are logged, never sent.
func (c *client) sendDomainError(channel, id string, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		c.sendError(channel, id, "invalid_command", ve.Message)
	case errors.Is(err, domain.ErrNotFound):
		c.sendError(channel, id, "not_found", "server not found")
	case errors.Is(err, domain.ErrServerNotRunning):
		c.sendError(channel, id, "server_not_running", "the server is not running")
	case errors.Is(err, domain.ErrContainerMissing), errors.Is(err, domain.ErrNotManaged):
		c.sendError(channel, id, "container_missing", "the server's container no longer exists")
	case errors.Is(err, domain.ErrDockerUnavailable):
		c.sendError(channel, id, "docker_unavailable", "Docker is not reachable")
	case errors.Is(err, domain.ErrDockerFailed):
		c.hub.log.Warn("websocket: docker error", "err", err)
		c.sendError(channel, id, "docker_error", "Docker rejected the operation")
	default:
		c.hub.log.Error("websocket: internal error", "err", err)
		c.sendError(channel, id, "internal_error", "internal server error")
	}
}
