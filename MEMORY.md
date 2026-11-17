# MEMORY.md — Mesquita Panel

Registro vivo de fatos do host, decisões arquiteturais e estado do projeto.
Atualizar sempre que algo aqui mudar. Spec completa: [`docs/SPEC.md`](docs/SPEC.md).

Última atualização: **2026-11-17**

---

## Host de produção (verificado em 2026-11-17)

O repositório está **no próprio servidor de produção**.

| Item | Valor |
|---|---|
| Hostname | `mesquita-server` |
| Arquitetura / kernel | x86_64 / Linux 7.0.0-43-generic |
| OS | Ubuntu Server 26.04.1 LTS |
| IP LAN | 192.168.15.11 |
| IP Tailscale | 100.126.54.17 |
| Domínio | brunomesquita.dev (Minecraft: minecraft.brunomesquita.dev) |
| Go | **não instalado** (build via container) |
| make | instalado pelo usuário (2026-11-17) |
| Docker | instalado; usuário `bruno-mesquita` **sem acesso** a `/var/run/docker.sock` |
| Dados Crafty | `/home/bruno-mesquita/docker/` — **não tocar** |
| Portas TCP ocupadas | 22, 80, 443, 6881, 7878, 8080, 8096, 8123, 8443, 9696, 25500–25600, 127.0.0.1:8787, 127.0.0.1:35353 |
| Porta livre escolhida | **8090** |

---

## Decisões

Formato: decisão — motivo — consequência.

- **D1 · Build em container Go pinado** — Go ausente no host e spec proíbe instalá-lo sem autorização. — `Makefile` usa `sudo docker run --rm -u $(id -u):$(id -g) golang:1.27.4-trixie`; caches em `.cache/` (gitignored) para não gerar arquivos de root; usuário digita a senha do sudo.
- **D2 · Porta padrão 8090, bind em `127.0.0.1` e `100.126.54.17`** — 8080 já está ocupada; painel não deve ser público. — `MESQUITA_LISTEN` aceita lista de endereços; nunca `0.0.0.0` por padrão.
- **D3 · Tailwind v4 via CLI standalone oficial, só em dev** — v4 não publica CSS precompilado com todas as utilities; `@tailwindcss/browser` não é recomendado em produção. — Binário pinado com SHA-256 em `tools/` (gitignored), `make css` gera `web/assets/css/app.css` commitado e embutido. Produção sem build, sem Node.
- **D4 · SQLite com `modernc.org/sqlite`** — pure Go, permite `CGO_ENABLED=0`. — WAL + `busy_timeout`; um pool de escrita com 1 conexão e pool de leitura separado para não bloquear a aplicação.
- **D5 · Docker Engine API via HTTP cru no unix socket** — SDK oficial traz árvore de dependências enorme; precisamos de ~10 endpoints. — Cliente fino em `internal/infrastructure/docker`, com allowlist e filtro por label.
- **D6 · WebSocket: `gorilla/websocket` atrás de adapter próprio** — spec exige lib externa + manager próprio. — Só `internal/infrastructure/websocket` importa gorilla; manager/roteamento são nossos.
- **D7 · Roteamento com `http.ServeMux` (padrões Go 1.22+)** — stdlib já cobre método + path params. — Sem chi/gorilla/mux.
- **D8 · Pacotes organizados por camada, não por feature** — a árvore da spec §16 mistura `internal/{domain,application,…}` com `internal/{auth,servers,…}`, o que duplicaria responsabilidades. — Features viram subpacotes dentro de cada camada (ex.: `internal/application/auth`).
- **D9 · Sessões no SQLite** — token aleatório de 32 bytes no cookie; armazenado só o hash SHA-256; expiração absoluta + por inatividade. Cookie `HttpOnly; SameSite=Strict; Secure` sob TLS. CSRF por token sincronizado com a sessão enviado em header `X-CSRF-Token`.
- **D11 · Token de setup de uso único** — sem ele, qualquer um na tailnet poderia criar o admin primeiro. — Gerado em memória a cada start enquanto não há admin, impresso no log (`journalctl`); `POST /api/v1/setup` exige o token.
- **D12 · CSRF em duas camadas** — `http.CrossOriginProtection` (stdlib, Sec-Fetch-Site/Origin) em toda a API cobre login/setup; `X-CSRF-Token` por sessão em todo método mutante autenticado.
- **D13 · Argon2id m=19 MiB, t=2, p=1 + semáforo de 2 hashes** — mínimo OWASP, pico de RAM baixo; verify lê parâmetros do hash armazenado e recusa parâmetros absurdos.
- **D14 · Timestamps INTEGER (Unix s, UTC) no SQLite; RFC 3339 na API** — portável para Rust, comparável em SQL.
- **D15 · Sem goroutines de fundo na Fase 1** — limpeza de sessões expiradas acontece no login; CPU/rede calculados por delta entre chamadas (cache de 2 s).
- **D10 · systemd com usuário dedicado `mesquita-panel`** — princípio do menor privilégio. — Precisa estar no grupo `docker` (root-equivalente) a partir da Fase 3; criação do usuário e grupo exige autorização no deploy.

---

## Pendências e riscos abertos

- [x] Acesso ao Docker para dev/test: sudo não funciona dentro do Claude Code (nem via `!`: sem TTY). Decisão (2026-11-17): o usuário roda os comandos num terminal próprio, com saída em `.cache/last-run.log`, que o Claude lê. Recusadas: Go em ~/.local, grupo docker.
- [x] Versões pinadas: Go `golang:1.27.4-trixie`, Tailwind CLI v4.3.5 (SHA-256 no Makefile), `modernc.org/sqlite` v1.61.1, `golang.org/x/crypto` v0.58.0.
- [x] `make` instalado. Fluxo: usuário roda `sudo make check` num terminal próprio; Claude lê `.cache/last-run.log`.
- [ ] Cookie `Secure` exige HTTPS: avaliar `tailscale serve` para TLS no acesso remoto.
- [x] `git init` feito (branch `main`); módulo `github.com/bruno-mesquita/mesquita-panel`. Falta `git config user.name/email` antes do primeiro commit.
- [ ] Confirmar o que ocupa 8080 (provável stack de mídia), só para documentação.

---

## Estado das fases

| Fase | Escopo | Status | Notas |
|---|---|---|---|
| 0 | Planejamento, CLAUDE.md, MEMORY.md, SPEC | ✅ concluída | 2026-11-17 |
| 1 | Foundation (config, SQLite, HTTP, embed, auth, dashboard inicial, systemd) | ✅ implementada e testada (2026-11-17); falta deploy systemd e conferir UI no navegador | ver medições abaixo |
| 2 | Monitoring (CPU, RAM, disco, rede, métricas de containers) | planejada | |
| 3 | Server management (Docker, criação, ciclo de vida, limites) | planejada | |
| 4 | WebSocket + console | planejada | |
| 5 | File manager | planejada | |
| 6 | Backups | planejada | |
| 7 | Game installers (Vanilla, Paper, Purpur, Fabric, Forge, NeoForge) | planejada | |

Legenda de detalhe por feature (preencher ao implementar): **implementado** / **testado** / **planejado**.

---

## Fase 1 — verificação (2026-11-17)

- `sudo make check`: gofmt, vet, todos os testes e build passaram. Binário estático de 12 MB.
- E2E com curl em `127.0.0.1:8090`: setup com token → 201; segundo setup → 409; login → cookie HttpOnly + csrf; stats reais; dashboard; POST sem CSRF → 403; cross-site → 403; listagem de diretório → 404; logout → 204, depois `me` → 401.
- Restart: banco persiste, setup não é mais exigido, token não é impresso, login ok. SIGTERM → shutdown gracioso.
- Memória: ~53 MB RSS depois de um login (o Argon2id aloca 19 MiB, que o heap do Go retém); 12 threads. Se incomodar, avaliar `GOMEMLIMIT` / `debug.FreeOSMemory` — medir antes.
- **Não verificado:** UI no navegador (renderização, navegação por teclado, contraste). Fazer com o painel em `make run` + túnel SSH.
- **Não feito:** deploy no systemd (seguir `docs/INSTALL.md`, precisa sudo e autorização).

## Roadmap — Fase 1 (Foundation, referência)

1. `go.mod` (`module github.com/<a definir>/mesquita-panel` ou nome local), `Makefile`, `.gitignore`; `git init` se autorizado.
2. Config via env (arquivo `/etc/mesquita-panel/config.env` em produção): `MESQUITA_LISTEN`, `MESQUITA_DATA_DIR`, `MESQUITA_DOCKER_SOCKET`, `MESQUITA_LOG_LEVEL`. Validação na inicialização com erro claro.
3. SQLite + runner de migrations embutidas; `migrations/0001_init.sql`: `schema_migrations`, `users`, `sessions`, `settings`, `audit_log`.
4. Auth: setup inicial (só enquanto não existe admin; depois 404/403), login/logout/me, Argon2id, rate limit por IP + usuário, CSRF, expiração de sessão, troca de senha, audit.
5. Servidor HTTP: middlewares (request id, slog, recover, security headers, auth, CSRF), `MaxBytesReader`, erros JSON padrão, shutdown gracioso; frontend via `embed`.
6. Frontend: shell (sidebar + navegação), páginas setup / login / overview; tokens `@theme`; componentes base (button, card, input, toast, dialog).
7. `GET /api/v1/system/stats` lendo `/proc` + `statfs` (CPU, RAM, disco, rede) — base do dashboard; cache curto.
8. `deployments/systemd/mesquita-panel.service` com hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `ReadWritePaths=/var/lib/mesquita-panel`, `PrivateTmp`) + `docs/INSTALL.md`, `docs/CONFIGURATION.md`, `docs/API.md` (parcial), `README.md`.
9. Testes: migrations (aplica do zero + idempotência), Argon2id, sessões, rate limit, CSRF, setup bloqueado, contrato HTTP com `httptest`.

**Aceite (spec §17):** compila para linux/amd64; roda sem Go; frontend servido pelo Go;
admin consegue logar; banco persiste após restart; Crafty continua operando (verificar com
`docker ps` com autorização do usuário, sem tocar nos containers).
