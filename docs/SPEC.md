# Craftkeeper — Complete Technical Specification

**Version:** 1.3  
**Status:** Approved for implementation  
**Project type:** Self-hosted Game Server Management Panel

---

# 1. Project Overview

Develop a lightweight, self-hosted game server management platform inspired by Crafty Controller and Pterodactyl.

The platform will provide centralized management of game servers, Docker containers, files, consoles, backups and system resources.

The primary objective is to create an efficient alternative with complete control over architecture and infrastructure.

## Core principles

1. Minimal RAM consumption.
2. Minimal CPU consumption when idle.
3. Go as the primary backend language.
4. HTML, CSS and Vanilla JavaScript for the frontend.
5. Tailwind CSS v4 for styling.
6. No React, Vue, Angular or other frontend frameworks.
7. No Node.js or npm runtime in production.
8. SOLID principles and Clean Architecture.
9. Replaceable infrastructure implementations.
10. Future compatibility with Rust migration.
11. Docker-based game server isolation.
12. Standalone Linux executable.
13. Security-first architecture.

---

# 2. Existing Infrastructure

The application will initially run on an existing Ubuntu server.

| Property | Value |
|---|---|
| Hostname | mesquita-server |
| OS | Ubuntu Server 26.04.1 LTS |
| Kernel | Linux 7.0.0-43-generic |
| Architecture | x86-64 |
| CPU | Intel Core i5-10400F |
| CPU | 6 cores / 12 threads |
| RAM | 14 GiB reported |
| GPU | NVIDIA GTX 1050 Ti |
| Disk | 238.5 GB |
| Docker | Installed |
| Crafty Controller | Installed and running |
| Tailscale | Installed and operational |
| Local IP | 192.168.15.11 |
| Tailscale IP | 100.126.54.17 |
| Domain | brunomesquita.dev |
| Minecraft address | minecraft.brunomesquita.dev |

## Existing services

- Minecraft Create: Eternal Journey (Forge).
- Minecraft Vanilla test instance.
- Crafty Controller.

Existing Crafty data:

`/home/bruno-mesquita/docker/`

## Critical production restrictions

The new application MUST NOT:

- Stop or recreate the Crafty container.
- Modify Crafty configurations.
- Modify existing Minecraft instances.
- Change existing Docker networks.
- Change existing port mappings.
- Delete or move existing server data.
- Take ownership of containers it did not create.

Initial deployment must run independently of Crafty.

---

# 3. Technology Stack

## Backend

- Go.
- Go standard library `net/http`.
- SQLite.
- Docker Engine API.
- Argon2id.
- `log/slog`.
- Gorilla WebSocket or another maintained WebSocket implementation.

Prefer pure-Go dependencies where practical.

## Frontend

- HTML5.
- CSS3.
- Tailwind CSS v4.
- Vanilla JavaScript.
- ES Modules.
- Native Web Components when appropriate.
- Fetch API.
- Native WebSocket API.
- SVG icons.

## Explicit exclusions

Do not use:

- React.
- Vue.
- Angular.
- Svelte.
- shadcn/ui.
- Bootstrap.
- jQuery.
- Node.js runtime.
- npm runtime.
- Vite or Webpack in production.

shadcn/ui is React-based and is not part of this architecture.

## Database

Use SQLite for:

- User accounts.
- Password hashes.
- Sessions.
- Server metadata.
- Server configurations.
- Backup metadata.
- Audit logs.
- Application settings.

Implement versioned database migrations.

Database operations must not block the entire application.

---

# 4. Frontend — Tailwind CSS v4

The frontend must use Tailwind CSS v4 as its primary styling system.

## Production strategy

Use the official precompiled Tailwind CSS v4 distribution.

Requirements:

- No Node.js installation.
- No npm installation.
- No Vite.
- No Webpack.
- No PostCSS pipeline.
- No Tailwind compilation step.
- Pin the Tailwind CSS version.
- Keep the CSS locally in the repository.
- Serve assets through Go.
- Embed assets into the executable using `embed`.

Do not depend on a public CDN in production.

The application must work offline after deployment.

If additional styling is necessary, use local CSS files.

> **Implementation note (2026-11-17, see MEMORY.md D3):** Tailwind v4 has no single
> precompiled CSS containing all utilities. Approved approach: the official standalone
> Tailwind CLI binary (no Node) runs only in development to generate the CSS, which is
> committed and embedded. Production has no build step.

## Design system

Create centralized design tokens using Tailwind CSS v4's CSS-first configuration approach.

Example:

```css
@import "./tailwind.css";

@theme {
    --color-background: #09090b;
    --color-surface: #18181b;
    --color-surface-hover: #27272a;

    --color-primary: #3b82f6;
    --color-success: #22c55e;
    --color-danger: #ef4444;

    --font-sans: Inter, sans-serif;
}
```

The actual import must point to the pinned local CSS distribution.

## Visual direction

- Dark-first interface.
- Professional game hosting dashboard aesthetic.
- Clean typography.
- Compact information density.
- Consistent spacing.
- Subtle borders.
- Responsive layouts.
- Accessible contrast.
- Clear visual hierarchy.

## Reusable components

Create a lightweight native component system.

Suggested components:

- Button.
- Card.
- Badge.
- Dialog.
- Dropdown.
- Sidebar.
- Navigation.
- Tabs.
- Tooltip.
- Input.
- Select.
- Table.
- Progress.
- Toast.
- Server status indicator.
- Console terminal.

Components should use native HTML, JavaScript modules and Web Components where appropriate.

Do not build an unnecessary custom frontend framework.

## Accessibility

- Semantic HTML.
- Keyboard navigation.
- Visible focus indicators.
- Accessible dialogs.
- Proper labels.
- ARIA where necessary.
- Reduced motion support.
- Sufficient color contrast.

## Frontend performance

- No unnecessary frontend runtime.
- No heavy component libraries.
- Lazy-load page modules when appropriate.
- Avoid unnecessary DOM updates.
- Avoid excessive WebSocket rendering.
- Batch high-frequency metric updates.
- Virtualize large log lists if necessary.
- Clean up event listeners and subscriptions.

---

# 5. Backend Architecture

Use Clean Architecture with four logical layers.

## Domain

Contains:

- Entities.
- Business rules.
- Value objects.
- Domain interfaces.
- Domain errors.

Must not depend on infrastructure or external frameworks.

## Application

Contains:

- Use cases.
- Application services.
- Workflow orchestration.
- Input/output ports.

Must depend on Domain abstractions.

## Infrastructure

Contains:

- Docker adapter.
- SQLite adapter.
- Filesystem adapter.
- Linux metrics adapter.
- WebSocket protocol adapter.

Implements interfaces defined by inner layers.

## Presentation

Contains:

- HTTP handlers.
- Routing.
- Request validation.
- Response serialization.
- WebSocket transport handlers.

Must not contain business rules.

---

# 6. SOLID Principles — Mandatory

All backend modules MUST follow SOLID.

## Single Responsibility

Every module must have one clearly defined responsibility.

Examples:

- Authentication handles authentication.
- DockerService handles Docker operations.
- MonitoringService handles system metrics.
- BackupService handles backups.
- FileService handles filesystem operations.

## Open/Closed

Components should be extensible without modifying unrelated existing components.

Adding a new game server implementation must not require rewriting the core management logic.

## Liskov Substitution

Implementations of interfaces must be interchangeable while preserving expected behavior.

## Interface Segregation

Prefer small, focused interfaces.

Example:

```go
type ContainerStarter interface {
    Start(ctx context.Context, id string) error
}

type ContainerStopper interface {
    Stop(ctx context.Context, id string) error
}
```

## Dependency Inversion

Business logic must depend on abstractions rather than concrete infrastructure.

Example:

```go
type ServerRepository interface {
    Get(ctx context.Context, id string) (Server, error)
    Save(ctx context.Context, server Server) error
}

type ContainerRuntime interface {
    Create(ctx context.Context, config ContainerConfig) (string, error)
    Start(ctx context.Context, id string) error
    Stop(ctx context.Context, id string) error
}
```

Use constructor-based dependency injection.

Avoid:

- Global mutable state.
- Hidden dependencies.
- Uncontrolled singletons.
- Excessive abstractions.
- Direct infrastructure calls from business logic.

---

# 7. Language Independence and Rust Migration

The architecture must not be tightly coupled to Go.

A future Rust migration should preserve:

- Frontend.
- REST API contracts.
- WebSocket message formats.
- Database schemas.
- Authentication semantics.
- Domain rules.
- Application use cases.

Use stable contracts:

- HTTP.
- JSON.
- WebSocket.
- Documented persistence schemas.

Do not expose Go-specific types through external contracts.

A Rust implementation must pass the same API, protocol and integration contract tests.

The frontend must remain unaware of the backend implementation language.

---

# 8. WebSocket Architecture — Mandatory

The application MUST implement its own WebSocket management module.

However, it MUST NOT implement the WebSocket protocol itself.

Use a maintained external library such as Gorilla WebSocket.

Our application owns connection management, authorization, message routing and communication policies.

## Architecture

```text
WebSocket Manager
       |
       v
Connection Abstraction
       |
       v
Protocol Adapter
       |
       v
External WebSocket Library
       |
       v
TCP Connection
```

## Manager responsibilities

- Connection registration.
- Connection removal.
- Authentication.
- Authorization.
- Session association.
- Channel management.
- Message routing.
- Broadcast operations.
- Lifecycle management.
- Bounded message queues.
- Backpressure handling.
- Connection cleanup.
- Graceful shutdown.

## Suggested interfaces

```go
type Connection interface {
    Read(ctx context.Context) ([]byte, error)
    Write(ctx context.Context, data []byte) error
    Close() error
}

type ConnectionManager interface {
    Register(conn Connection)
    Unregister(conn Connection)
}

type MessageRouter interface {
    Publish(channel string, message Message) error
    Subscribe(connectionID string, channel string) error
    Unsubscribe(connectionID string, channel string) error
}
```

These are illustrative contracts. Refine them during implementation to avoid unnecessary coupling.

## Protocol responsibilities

External library handles:

- Handshake.
- Frame encoding and decoding.
- Masking.
- Protocol compliance.
- Ping and pong.
- Low-level connection operations.

Our implementation handles:

- Authentication.
- Authorization.
- Subscriptions.
- Message routing.
- Event distribution.
- Application-level communication.

Never recreate WebSocket framing, TCP transport or protocol negotiation.

## Message format

All application messages must use documented, versioned formats.

Example:

```json
{
  "version": 1,
  "type": "console.output",
  "channel": "server:123",
  "payload": {
    "data": "Server started successfully"
  }
}
```

Supported message categories:

- Console output.
- Console commands.
- Server status.
- Resource metrics.
- Connection errors.
- Subscription events.

## Security and performance

- Authenticate connections.
- Authorize subscriptions.
- Validate message sizes.
- Limit concurrent connections.
- Use bounded queues.
- Apply write deadlines.
- Clean up disconnected clients.
- Prevent unauthorized cross-server subscriptions.
- Validate Origin where appropriate.
- Avoid unbounded goroutines.
- Avoid unbounded buffers.
- Avoid unnecessary broadcasts.

## Rust migration

Preserve:

- Message schemas.
- Event types.
- Channel semantics.
- Authorization behavior.
- Endpoints.
- Connection lifecycle guarantees.

Only internal implementations should need replacement.

---

# 9. Frontend Features

## Main navigation

- Overview.
- Servers.
- Backups.
- Users.
- Settings.

## Dashboard

Display:

- CPU utilization.
- RAM utilization.
- Disk utilization.
- Network traffic.
- Running servers.
- Stopped servers.
- Recent events.

Metrics should update without refreshing the entire page.

## Server management

Each server contains:

- Name.
- Description.
- Game type.
- Version.
- Status.
- Memory limit.
- CPU limit.
- Container ID.
- Creation date.

Actions:

- Start.
- Stop.
- Restart.
- Kill.
- Delete.

Require confirmation for destructive operations.

## Console

- Real-time output.
- Command input.
- Auto-scroll.
- Connection status.
- Reconnection.
- Multiple simultaneous consoles.

Use the custom WebSocket Manager.

## File manager

- List files and directories.
- Navigate directories.
- Upload.
- Download.
- Create directories.
- Rename.
- Delete.
- Edit text files.

Prevent path traversal and symlink escapes.

## Backups

- Create.
- List.
- Restore.
- Delete.
- Display size and creation date.

Use streaming or filesystem operations rather than loading entire server directories into RAM.

---

# 10. REST API

Use versioned REST endpoints and JSON responses.

## Authentication

```text
POST /api/v1/auth/login
POST /api/v1/auth/logout
GET  /api/v1/auth/me
```

## Dashboard

```text
GET /api/v1/system/stats
GET /api/v1/dashboard
```

## Servers

```text
GET    /api/v1/servers
POST   /api/v1/servers
GET    /api/v1/servers/:id
DELETE /api/v1/servers/:id

POST /api/v1/servers/:id/start
POST /api/v1/servers/:id/stop
POST /api/v1/servers/:id/restart
POST /api/v1/servers/:id/kill
```

## WebSocket

```text
/api/v1/ws
```

Use documented subscription messages to access server consoles and monitoring channels.

## Files

```text
GET    /api/v1/servers/:id/files
GET    /api/v1/servers/:id/files/download
POST   /api/v1/servers/:id/files/upload
PUT    /api/v1/servers/:id/files
DELETE /api/v1/servers/:id/files
```

## Backups

```text
GET    /api/v1/servers/:id/backups
POST   /api/v1/servers/:id/backups
POST   /api/v1/servers/:id/backups/:backupID/restore
DELETE /api/v1/servers/:id/backups/:backupID
```

All endpoints require validation, authentication and authorization.

Use consistent JSON error responses.

---

# 11. Docker Integration

Use Docker Engine API through the local Unix socket.

Never expose unrestricted Docker API access to frontend clients.

Required operations:

- List managed containers.
- Create containers.
- Start containers.
- Stop containers.
- Restart containers.
- Remove containers.
- Read container logs.
- Execute authorized console operations.
- Retrieve container statistics.

## Container identification

Use Docker labels:

```text
mesquita.managed=true
mesquita.server_id=<server-id>
mesquita.game=minecraft
```

> **Implementation note (2026-11-20, see MEMORY.md D69):** after the rename to Craftkeeper the
> labels are `craftkeeper.managed`, `craftkeeper.server_id` and `craftkeeper.game`; the
> `mesquita.*` labels above are no longer recognised.

The application must never assume every container belongs to Craftkeeper.

Docker socket access is root-equivalent.

Apply strict operation allowlists, authorization and validation.

Never accept arbitrary host shell commands from API requests.

---

# 12. Resource Efficiency

Resource efficiency is a core requirement.

- Avoid unnecessary goroutines.
- Use bounded buffers.
- Stream large files.
- Cache metrics briefly.
- Configure monitoring intervals.
- Avoid excessive database queries.
- Implement graceful shutdown.
- Release disconnected WebSocket resources.
- Avoid unnecessary dependencies.

Benchmark actual resource consumption instead of promising arbitrary RAM limits.

---

# 13. Authentication and Security

- Local administrator account.
- Argon2id password hashing.
- Secure session cookies.
- HttpOnly.
- SameSite protection.
- CSRF protection.
- Login rate limiting.
- Session expiration.
- Password changes.
- Audit logs.

Default to localhost or explicitly configured private interfaces.

Remote access should initially use Tailscale or a properly configured secure reverse proxy.

Do not expose the administrative interface publicly by default.

---

# 14. Deployment

Go is NOT installed on the production Ubuntu server.

The preferred deployment strategy is cross-compilation from a development machine or CI environment.

Target:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath \
  -ldflags="-s -w" \
  -o craftkeeper ./cmd/panel
```

The frontend must be embedded using Go `embed`.

Production must not require:

- Go installation.
- Node.js.
- npm.
- Frontend build tools.
- Separate frontend server.

## Suggested directories

```text
/opt/craftkeeper/
    craftkeeper

/etc/craftkeeper/
    config.env

/var/lib/craftkeeper/
    database.sqlite
    backups/
    logs/
```

Use systemd.

Proposed default HTTP port: 8080.

> **Implementation note (2026-11-17, see MEMORY.md D2):** port 8080 is already in use on
> mesquita-server. Default port is **8090**.

Verify port availability before installation.

Do not modify existing Crafty ports.

## Installation guide

Provide instructions for:

1. Checking architecture.
2. Checking Docker availability.
3. Checking ports.
4. Creating directories.
5. Transferring the binary.
6. Configuring environment variables.
7. Creating the systemd service.
8. Starting the service.
9. Checking logs with journalctl.
10. Initial administrator setup.

Do not install Go on production unless explicitly authorized.

---

# 15. Initial Setup Workflow

On first execution:

1. Initialize database.
2. Execute migrations.
3. Check Docker connectivity.
4. Check filesystem permissions.
5. Provide administrator setup.
6. Mark setup completed.
7. Require authentication.

Never ship a default administrator password.

Prevent public access to the setup process after initialization.

---

# 16. Suggested Project Structure

```text
craftkeeper/
│
├── cmd/
│   └── panel/
│       └── main.go
│
├── internal/
│   ├── domain/
│   ├── application/
│   ├── infrastructure/
│   │   ├── docker/
│   │   ├── sqlite/
│   │   ├── filesystem/
│   │   ├── metrics/
│   │   └── websocket/
│   │       └── gorilla/
│   │
│   ├── presentation/
│   │   ├── http/
│   │   └── websocket/
│   │
│   ├── auth/
│   ├── servers/
│   ├── monitoring/
│   ├── files/
│   └── backups/
│
├── web/
│   ├── index.html
│   ├── tailwind.css
│   └── assets/
│       ├── css/
│       ├── js/
│       └── icons/
│
├── migrations/
├── deployments/
│   └── systemd/
│
├── docs/
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

The exact package boundaries can be refined during implementation while preserving dependency rules.

> **Implementation note (2026-11-17, see MEMORY.md D8):** packages are organized by layer
> only; the per-feature folders (`internal/auth`, `internal/servers`, …) are dropped to
> avoid duplicating the layer folders. Features live as sub-packages inside each layer.

---

# 17. Development Phases

## Phase 1 — Foundation

- Go project initialization.
- Clean Architecture.
- Configuration management.
- SQLite.
- HTTP server.
- Embedded frontend.
- Authentication.
- Initial dashboard.
- Systemd deployment.

Acceptance:
- Compiles for Linux AMD64.
- Runs without Go installed on Ubuntu.
- Frontend is served by Go.
- Administrator can log in.
- Database persists.
- Crafty remains operational.

## Phase 2 — Monitoring

- CPU.
- RAM.
- Disk.
- Network.
- Docker container metrics.

## Phase 3 — Server Management

- Docker integration.
- Server creation.
- Container lifecycle.
- Resource limits.
- Metadata.

## Phase 4 — WebSocket and Console

- Custom WebSocket Manager.
- Protocol adapter.
- Authentication.
- Subscription routing.
- Real-time logs.
- Command input.
- Reconnection.
- Backpressure.

## Phase 5 — File Manager

- Directory navigation.
- Upload/download.
- Editing.
- Security restrictions.

## Phase 6 — Backups

- Creation.
- Listing.
- Restoration.
- Deletion.

## Phase 7 — Game Installers

Support:

- Vanilla.
- Paper.
- Purpur.
- Fabric.
- Forge.
- NeoForge.

Use configurable, verified download sources.

Do not implement automatic modpack installation until basic server lifecycle management is stable.

---

# 18. Testing Requirements

Include:

- Unit tests.
- API contract tests.
- Integration tests.
- Docker adapter tests.
- Authentication tests.
- Filesystem security tests.
- WebSocket lifecycle tests.
- WebSocket protocol contract tests.
- Database migration tests.

Use mocks for infrastructure dependencies where appropriate.

Tests must never manipulate production Crafty containers.

---

# 19. Documentation Deliverables

Produce:

- README.md.
- ARCHITECTURE.md.
- INSTALL.md.
- CONFIGURATION.md.
- API.md.
- SECURITY.md.
- DEVELOPMENT.md.
- WEBSOCKET.md.

Document external contracts and operational procedures.

Clearly distinguish implemented, tested and planned functionality.

---

# 20. Definition of Done

The initial release must:

- Be written in Go.
- Use HTML, CSS, Vanilla JavaScript and Tailwind CSS v4.
- Require no React or other frontend framework.
- Require no Node.js runtime in production.
- Use locally stored, pinned Tailwind CSS assets.
- Embed frontend assets in the Go binary.
- Compile for Linux AMD64.
- Run without Go installed on Ubuntu.
- Follow SOLID and Clean Architecture.
- Include replaceable infrastructure adapters.
- Include a custom WebSocket Manager.
- Use an external WebSocket protocol implementation.
- Provide administrator authentication.
- Display real system metrics.
- Integrate with Docker.
- Preserve existing Crafty installations.
- Include systemd deployment.
- Include installation documentation.

---

# 21. Final Implementation Instructions

Prioritize correctness, security, modularity and resource efficiency.

Do not implement every phase simultaneously.

Start with Phase 1 and deliver a working, deployable foundation.

Do not install software, modify system configurations, stop containers or change network settings on production without explicit authorization.

All important architectural decisions must be documented.

SOLID and Clean Architecture are mandatory but must not become excuses for unnecessary abstractions.

The application must remain replaceable at the infrastructure and implementation levels, including a potential future migration from Go to Rust.

**Primary objective:** Build an independent, lightweight alternative to Crafty Controller and Pterodactyl, optimized for a single Ubuntu host with a future path toward multi-node game hosting.
