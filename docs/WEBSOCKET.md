# WebSocket protocol — v1

Live server console and status. This is a public contract (spec §7, §8): message schemas,
types, channel semantics, error codes, close codes and limits must stay compatible.

## Endpoint and authentication

`GET /api/v1/ws` — WebSocket handshake.

- Requires the session cookie (same as the REST API). Without it: `401 unauthenticated`.
- The `Origin` header is required and must be the panel itself (scheme `http`/`https`, host
  equal to the request `Host`). Otherwise: `403 origin_not_allowed`. Together with the
  `SameSite=Strict` session cookie, this prevents cross-site WebSocket hijacking.
- A request that is not a valid WebSocket handshake: `400 bad_handshake`.
- Too many connections: `429 too_many_connections`. Shutting down: `503 shutting_down`.
- Handshake errors use the REST error body `{"error":{"code","message"}}`.

The session is re-checked every 60 s and before every console command. A session that expired
or was revoked (logout, password change) closes the connection with code **4401**. While a
connection is open the session's idle timer is refreshed.

## Messages

Every message is a JSON text frame (binary frames close the connection with 1003):

```json
{"version": 1, "type": "console.output", "channel": "server:<id>", "id": "c1", "payload": {}}
```

- `version`: always `1`. Any other value is answered with `unsupported_version`.
- `type`: see below.
- `channel`: `server:<server id>` — the only channel kind in v1.
- `id`: optional, at most 64 characters, chosen by the client. Replies to that message
  (`subscribed`, `unsubscribed`, `ack`, `error`) carry the same `id`.
- `payload`: always an object in server messages.

Unknown fields are ignored. Times are RFC 3339 UTC with up to nanosecond precision.

### Client → server

| type | channel | payload | reply |
|---|---|---|---|
| `subscribe` | `server:<id>` | — | `subscribed`, then `console.history` and `server.status` |
| `unsubscribe` | `server:<id>` | — | `unsubscribed` |
| `console.command` | `server:<id>` | `{"command": "say hello"}` | `ack` or `error` |

- Subscribing to a channel already subscribed only answers `subscribed` again.
- At most 16 subscriptions per connection (`too_many_subscriptions`).
- `console.command` does not require a subscription. `command`: 1–1024 bytes of UTF-8,
  without control characters (no line breaks): it is written as one line to the game's
  console. The server must be `running` or `starting`. Every command is recorded in the audit
  log (`server.console_command`, target `<server name>: <command, truncated to 200 bytes>`).
  `ack` means the command was delivered to the game, not that it succeeded; its output appears
  as `console.output`.

### Server → client

| type | payload |
|---|---|
| `subscribed` | `{}` |
| `unsubscribed` | `{}` |
| `console.history` | `{"lines": [Line]}` — up to the last 500 lines, oldest first |
| `console.output` | `Line` — one new line |
| `server.status` | `{"status": "running"}` — same values as the REST `Server.status` |
| `ack` | `{}` |
| `error` | `{"code": "snake_case", "message": "human readable"}` |

`Line` is `{"time": "2026-11-17T10:00:00.123456789Z", "stream": "stdout"|"stderr", "data": "text"}`.
`time` is omitted when unknown. `data` is plain text (never HTML) without the line break;
lines longer than 4 KiB are truncated.

### Ordering and delivery guarantees

- For one channel, `subscribed` comes first, then `console.history`, then `server.status`,
  then live `console.output` / `server.status` in order. No line appears in both the history and
  the live output.
- `server.status` is sent on subscription and whenever the status changes (derived from Docker
  events, re-read from Docker each time). After the panel loses and regains the Docker event
  stream, the status of every subscribed server is sent again if it changed.
- After a server stops, its console stays subscribed; output resumes when it starts again,
  without repeating lines.
- Messages are not replayed across connections. After reconnecting, the client subscribes
  again and receives a fresh `console.history`.

### Error codes

| code | meaning |
|---|---|
| `invalid_message` | not JSON, `id` too long, or a malformed payload |
| `unsupported_version` | `version` is not 1 |
| `unknown_type` | unknown message type |
| `invalid_channel` | channel is not `server:<valid id>` |
| `not_found` | the server does not exist |
| `not_subscribed` | `unsubscribe` of a channel not subscribed |
| `too_many_subscriptions` | more than 16 subscriptions |
| `invalid_command` | the command fails validation |
| `server_not_running` | the server is stopped or restarting |
| `container_missing` | the server's container no longer exists |
| `docker_unavailable` | Docker is not reachable |
| `docker_error` | Docker rejected the operation |
| `internal_error` | unexpected failure (details only in the panel log) |

Errors never close the connection, except as listed below.

## Close codes

| code | meaning | client should |
|---|---|---|
| 1000 | normal closure | — |
| 1001 | the panel is shutting down | reconnect with backoff |
| 1003 | binary message received | fix the client |
| 1009 | message larger than 4 KiB | fix the client |
| 1013 | the client could not keep up with its outgoing queue | reconnect with backoff |
| 4401 | session expired or revoked | log in again; do not reconnect |

## Limits

| limit | value |
|---|---|
| connections | 64 in total, 8 per user |
| client message size | 4 KiB |
| subscriptions per connection | 16 |
| outgoing queue per connection | 512 messages; when full the connection is closed (1013) |
| write deadline | 10 s per message |
| keepalive | server pings every 30 s; a connection silent for 60 s is dropped |
| session re-check | every 60 s and before each command |

Clients should reconnect with exponential backoff and jitter (the panel UI uses 1 s up to
30 s), and re-subscribe their channels.
