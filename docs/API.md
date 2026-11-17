# REST API — v1

Base path: `/api/v1`. JSON in and out (`Content-Type: application/json`). This document is a
contract: a different backend implementation (e.g. Rust) must behave identically.
Status: **implemented and covered by contract tests** (`internal/presentation/http/api_test.go`)
unless marked *planned*.

## Conventions

- Timestamps: RFC 3339, UTC (`2026-11-17T12:00:00Z`). IDs: opaque strings.
- Request bodies: a single JSON object, max 64 KiB, unknown fields rejected.
- Authentication: `mesquita_session` cookie (`HttpOnly`, `SameSite=Strict`, `Secure` under TLS),
  set by login.
- CSRF: every authenticated non-GET request must send `X-CSRF-Token: <csrf_token>` (returned by
  login and `/auth/me`). In addition, all cross-origin state-changing requests are rejected
  (`Sec-Fetch-Site`/`Origin` check) with `403` plain text.
- All API responses carry `Cache-Control: no-store`.

### Errors

```json
{ "error": { "code": "validation_failed", "message": "must be 3 to 32 characters", "field": "username" } }
```

`field` is present only for `validation_failed`.

| Status | `code` | Meaning |
|---|---|---|
| 400 | `invalid_json` | Body is not one valid JSON object, or has unknown fields |
| 401 | `unauthenticated` | Missing, expired or revoked session |
| 401 | `invalid_credentials` | Wrong username/password (never says which) |
| 403 | `csrf_invalid` | Missing or wrong `X-CSRF-Token` |
| 403 | `setup_token_invalid` | Wrong setup token |
| 404 | `not_found` | Unknown endpoint or resource |
| 409 | `setup_completed` | Setup already done |
| 413 | `payload_too_large` | Body over 64 KiB |
| 422 | `validation_failed` | Field-level validation error |
| 429 | `rate_limited` | Too many failed logins; retry after the window |
| 500 | `internal_error` | Unexpected server error (details only in logs) |

## Objects

**User**
```json
{ "id": "string", "username": "admin", "role": "admin", "created_at": "2026-11-17T12:00:00Z" }
```

**Session**
```json
{ "user": { /* User */ }, "csrf_token": "string" }
```

## Endpoints

### `GET /health` — public
`200 {"status":"ok"}`

### `GET /setup` — public
`200 {"required": true|false}`

### `POST /setup` — public, only while setup is required
Body: `{"token": "...", "username": "...", "password": "..."}`.
The one-time token is printed to the service log at startup while setup is pending.
Username: 3–32 chars of `[A-Za-z0-9._-]`, case-insensitive unique. Password: 12–256.
`201 {"user": User}` · `403 setup_token_invalid` · `409 setup_completed` · `422`.

### `POST /auth/login` — public
Body: `{"username": "...", "password": "..."}`.
`200 Session` + `Set-Cookie` · `401 invalid_credentials` · `429 rate_limited`.

### `POST /auth/logout` — auth + CSRF
`204`, clears the cookie and deletes the session.

### `GET /auth/me` — auth
`200 Session`.

### `POST /auth/password` — auth + CSRF
Body: `{"current_password": "...", "new_password": "..."}`.
`204`; every other session of the user is revoked · `401 invalid_credentials` · `422`.

### `GET /system/stats` — auth
Cached for 2 s. Network totals exclude `lo`, `veth*`, `docker*`, `br-*`, `virbr*`.
```json
{
  "collected_at": "2026-11-17T12:00:00.123Z",
  "cpu": { "cores": 12, "usage_percent": 7.5 },
  "memory": { "total_bytes": 0, "used_bytes": 0, "available_bytes": 0 },
  "disk": { "path": "/var/lib/mesquita-panel", "total_bytes": 0, "used_bytes": 0, "free_bytes": 0 },
  "network": { "rx_bytes": 0, "tx_bytes": 0, "rx_bytes_per_second": 0, "tx_bytes_per_second": 0 }
}
```

### `GET /dashboard` — auth
```json
{
  "docker": { "reachable": true },
  "recent_events": [
    { "id": 2, "created_at": "...", "action": "auth.login", "target": "admin", "ip": "127.0.0.1", "success": true }
  ]
}
```
Audit actions: `auth.setup`, `auth.login`, `auth.logout`, `auth.password_change`.
Server counts are added in phase 3.

## Planned

Servers (phase 3), WebSocket `/ws` (phase 4, see `WEBSOCKET.md`), files (phase 5),
backups (phase 6) — as listed in `docs/SPEC.md` §10.
