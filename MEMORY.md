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
| Docker | Engine 29.10.0, API 1.57 (mínima 1.40), containerd v2.4.2; usuário `bruno-mesquita` **sem acesso** a `/var/run/docker.sock` |
| Dados Crafty | `/home/bruno-mesquita/docker/` — **não tocar** |
| Portas TCP ocupadas | 22, 80, 443, 6881, 7878, 8080, 8096, 8123, 8443, 9696, 25500–25600, 127.0.0.1:8787, 127.0.0.1:35353 |
| Porta livre escolhida | **8090** |

---

## Decisões

Formato: decisão — motivo — consequência.

- **D1 · Build em container Go pinado** — Go ausente no host e spec proíbe instalá-lo sem autorização. — Usuário roda `sudo make check` num terminal próprio (sudo não funciona dentro do Claude Code); o Makefile usa `SUDO_UID/SUDO_GID` para o container rodar como o usuário real (sem arquivos de root), caches em `.cache/`, saída em `.cache/last-run.log` (Claude lê esse arquivo).
- **D2 · Porta padrão 8090, bind em `127.0.0.1` e `100.126.54.17`** — 8080 já está ocupada; painel não deve ser público. — `MESQUITA_LISTEN` aceita lista de endereços; nunca `0.0.0.0` por padrão.
- **D3 · Tailwind v4 via CLI standalone oficial, só em dev** — v4 não publica CSS precompilado com todas as utilities; `@tailwindcss/browser` não é recomendado em produção. — Binário pinado com SHA-256 em `tools/` (gitignored), `make css` gera `web/assets/css/app.css` commitado e embutido. Produção sem build, sem Node.
- **D4 · SQLite com `modernc.org/sqlite`** — pure Go, permite `CGO_ENABLED=0`. — WAL + `busy_timeout`; um pool de escrita com 1 conexão e pool de leitura separado para não bloquear a aplicação.
- **D5 · Docker Engine API via HTTP cru no unix socket** — SDK oficial traz árvore de dependências enorme; precisamos de ~10 endpoints. — Cliente fino em `internal/infrastructure/docker`, com allowlist e filtro por label.
- **D6 · WebSocket: `gorilla/websocket` atrás de adapter próprio** — spec exige lib externa + manager próprio. — Só `internal/infrastructure/websocket` importa gorilla; manager/roteamento são nossos.
- **D7 · Roteamento com `http.ServeMux` (padrões Go 1.22+)** — stdlib já cobre método + path params. — Sem chi/gorilla/mux.
- **D8 · Pacotes organizados por camada, não por feature** — a árvore da spec §16 mistura `internal/{domain,application,…}` com `internal/{auth,servers,…}`, o que duplicaria responsabilidades. — Features viram subpacotes dentro de cada camada (ex.: `internal/application/auth`).
- **D9 · Sessões no SQLite** — token aleatório de 128 bits (`crypto/rand.Text()`) no cookie; armazenado só o hash SHA-256; expiração absoluta + por inatividade. Cookie `HttpOnly; SameSite=Strict; Secure` sob TLS. CSRF por token sincronizado com a sessão enviado em header `X-CSRF-Token`.
- **D11 · Token de setup de uso único** — sem ele, qualquer um na tailnet poderia criar o admin primeiro. — Gerado em memória a cada start enquanto não há admin, impresso no log (`journalctl`); `POST /api/v1/setup` exige o token.
- **D12 · CSRF em duas camadas** — `http.CrossOriginProtection` (stdlib, Sec-Fetch-Site/Origin) em toda a API cobre login/setup; `X-CSRF-Token` por sessão em todo método mutante autenticado.
- **D13 · Argon2id m=19 MiB, t=2, p=1 + semáforo de 2 hashes** — mínimo OWASP, pico de RAM baixo; verify lê parâmetros do hash armazenado e recusa parâmetros absurdos.
- **D14 · Timestamps INTEGER (Unix s, UTC) no SQLite; RFC 3339 na API** — portável para Rust, comparável em SQL.
- **D15 · Sem goroutines de fundo na Fase 1** — limpeza de sessões expiradas acontece no login; CPU/rede calculados por delta entre chamadas (cache de 2 s).
- **D16 · Engine API fixa em `/v1.44`** — Docker Engine 25+, já tem stats `one-shot` (≥ 1.41); sem negociação via `/version`. Host: Engine 29.10.0, API 1.57 (mínima 1.40), verificado em 2026-11-17. — Revisar só se um daemon futuro subir a versão mínima acima de 1.44.
- **D17 · Métricas só de containers `mesquita.managed=true`** (escolha do usuário, 2026-11-17) — spec §11. — Filtro no daemon **e** revalidação da label + formato do ID no cliente. Ainda não cruza `mesquita.server_id` com o banco: não existe tabela `servers` até a Fase 3, que adiciona esse cruzamento antes de qualquer ação.
- **D18 · Sem histórico de métricas** (escolha do usuário) — só tempo real por polling REST (3 s); a Fase 4 levou só status e console para o WebSocket (escolha do usuário), métricas seguem por polling; sem goroutine de fundo. CPU/rede de container por delta entre coletas (primeira amostra = 0), mapa de amostras podado a cada coleta.
- **D19 · Imagem de jogo `itzg/minecraft-server:2026.11.0` (Java 25) pinada por digest** (escolha do usuário, 2026-11-17) — baixa o jar sozinha via `TYPE`/`VERSION`; a Fase 7 vira ampliar a allowlist de loaders. Digest do índice multi-arch `sha256:8d5080d8dcd0a2629c2e4f248678373a337a40537ad6ef2857c7dbcda6e3eadd` (Docker Hub, verificado 2026-11-17). Java 25 e não 21: o Minecraft atual exige 25. Versões antigas (< 1.17) podem precisar de outra tag de Java — tratar na Fase 7. Atualizar = trocar `imageTag`/`imageDigest` em `docker/runtime.go`.
- **D20 · Portas de jogo `25601–25700` (`MESQUITA_GAME_PORTS`), bind em `0.0.0.0`** (escolha do usuário) — Crafty usa 25500–25600. Porta = pedida ou primeira livre (não usada no banco + `net.Listen` passa); UNIQUE no banco; `port_in_use` no start cobre a corrida.
- **D21 · Política fixa do container** — roda como uid:gid do painel (arquivos do painel → Fases 5/6); **nunca root**: com o painel como root (`sudo make run`) usa `1000:1000` e a pasta do servidor é `chown`ada para ele (sem capabilities, o entrypoint root do itzg falha no `chown`/troca de usuário e entra em loop de restart — visto em 2026-11-17), `CapDrop ALL`, `no-new-privileges`, swap = 0, PIDs 2048, logs 3 × 10 MB, `unless-stopped`, stop 60 s, `OpenStdin` (console da Fase 4 sem recriar), bridge padrão (sem `docker network`). Heap = `MaxRAMPercentage=75` com `MEMORY=""`: mudar limite + restart já vale. `ENABLE_RCON=false` (console usa stdin, D25).
- **D22 · Status derivado, nunca persistido** — `running/starting/stopped/restarting/missing/unknown`; um container só conta se ID **e** label `server_id` batem com o banco. Toda operação mutante do `docker.Runtime` faz inspect e confere `mesquita.managed` + `server_id` antes de agir (D17 cumprido).
- **D23 · Delete opt-in de arquivos** (escolha do usuário) — sem `delete_files=true` a pasta vai para `servers/.deleted/<id>-<unix>`. Remoção/arquivamento via `os.Root`. Container que sumiu ou não é mais nosso é só esquecido, nunca tocado.
- **D24 · Prazos por chamada no cliente Docker** — sem timeout global; 5 s padrão, stop/restart 75 s, pull 10 min. Handlers longos estendem read+write deadline com `http.ResponseController` (o read deadline vencido cancela o contexto da requisição).
- **D25 · Entrada do console por attach em stdin** — uma conexão curta por comando (`POST /containers/{id}/attach?stream=1&stdin=1`, `Upgrade: tcp`, 101 → `cmd\n` → fecha); containers já têm `OpenStdin` (D21), sem RCON. — **Verificado com servidor real (2026-11-17):** o `mc-server-runner` do itzg repassa stdin sem TTY; comandos chegam ao jogo.
- **D26 · Saída por `logs?follow=1&timestamps=1`, um follower por servidor** — criado no 1º inscrito, cancelado no último; ring buffer de 500 linhas (linha ≤ 4 KiB) enviado como histórico; reabre com `since=<última>+1ns` (o `since` do Docker é inclusivo), sem duplicar linhas; rechecagem a cada 30 s caso um evento `start` se perca.
- **D27 · Status por Docker events** — uma goroutine de vida do processo (`console.Service.Run`), filtro `type=container` + label managed + start/die/stop/restart/destroy/health_status; backoff 2 s→30 s; ressincroniza ao reconectar. Status sempre re-derivado por `servers.Service.Get` (D22); só consulta servidores com inscritos.
- **D28 · Limites fixos do WS** — 64 conexões / 8 por usuário (429), mensagem ≤ 4 KiB (1009), 16 inscrições, fila de 512 mensagens (cheia → 1013), write deadline 10 s, ping 30 s / silêncio 60 s, sessão revalidada a cada 60 s e antes de cada comando (4401). Console aberto renova a sessão.
- **D29 · Anti-CSWSH** — `Origin` obrigatório e igual ao `Host` + cookie `SameSite=Strict`; sem token CSRF na URL. Exceção: `_test.go` de `presentation/websocket` importa o client do gorilla só como par de teste.
- **Dependência:** `github.com/gorilla/websocket` v1.5.3 (spec §8, D6), só em `internal/infrastructure/websocket`.
- **D10 · systemd com usuário dedicado `mesquita-panel`** — princípio do menor privilégio. — Precisa estar no grupo `docker` (root-equivalente) a partir da Fase 3; criação do usuário e grupo exige autorização no deploy.

---

## Pendências e riscos abertos

- [x] Acesso ao Docker para dev/test: sudo não funciona dentro do Claude Code (nem via `!`: sem TTY). Decisão (2026-11-17): o usuário roda os comandos num terminal próprio, com saída em `.cache/last-run.log`, que o Claude lê. Recusadas: Go em ~/.local, grupo docker.
- [x] Versões pinadas: Go `golang:1.27.4-trixie`, Tailwind CLI v4.3.5 (SHA-256 no Makefile), `modernc.org/sqlite` v1.61.1, `golang.org/x/crypto` v0.58.0.
- [x] `make` instalado. Fluxo: usuário roda `sudo make check` num terminal próprio; Claude lê `.cache/last-run.log`.
- [ ] Cookie `Secure` exige HTTPS: avaliar `tailscale serve` para TLS no acesso remoto.
- [x] Git: branch `main`, módulo `github.com/bruno-mesquita/mesquita-panel`, identidade configurada. Commits (Fases 1 e 2) **só locais** — não fazer push sem o usuário pedir (ele ainda vai instalar o `gh`).
- [ ] `.data/` local contém um banco de teste (usuário `bruno`, senha `senha-de-teste-123`) — rodar `make clean` antes de testar a UI de verdade.
- [ ] Confirmar o que ocupa 8080 (provável stack de mídia), só para documentação.

---

## Estado das fases

| Fase | Escopo | Status | Notas |
|---|---|---|---|
| 0 | Planejamento, CLAUDE.md, MEMORY.md, SPEC | ✅ concluída | 2026-11-17 |
| 1 | Foundation (config, SQLite, HTTP, embed, auth, dashboard inicial, systemd) | ✅ implementada e testada (2026-11-17); UI conferida no navegador; falta deploy systemd | ver medições abaixo |
| 2 | Monitoring (CPU, RAM, disco, rede, métricas de containers) | ✅ implementada e verificada (2026-11-17) | ver Fase 2 abaixo |
| 3 | Server management (Docker, criação, ciclo de vida, limites) | ✅ implementada, testada e verificada com servidor real (2026-11-17) | ver Fase 3 abaixo |
| 4 | WebSocket + console | ✅ implementada, testada e verificada com servidor real (2026-11-17) | ver Fase 4 abaixo |
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
- UI no navegador: abriu e funcionou pelo Tailscale (`make run RUN_LISTEN=127.0.0.1:8090,100.126.54.17:8090`), confirmado pelo usuário em 2026-11-17. Ainda não auditado: navegação por teclado e contraste.
- Acesso: `make run` só escuta em 127.0.0.1 por padrão; do PC do usuário, usar Tailscale (`RUN_LISTEN`) ou túnel SSH. `localhost` no PC ≠ servidor.
- **Não feito:** deploy no systemd (seguir `docs/INSTALL.md`, precisa sudo e autorização).
- **Desvios do roadmap:** sem middleware de request id; componente dialog não criado (não havia uso — criar quando aparecer ação destrutiva, Fase 3).

## Fase 2 — estado (2026-11-17)

- **Implementado:** `GET /api/v1/system/containers` (`docs/API.md`); `docker.StatsCollector`
  (`internal/infrastructure/docker/stats.go`: list com filtro de label + stats `one-shot`,
  concorrência 4, body ≤ 64 KiB por stats e ≤ 1 MiB na listagem); `monitoring.Service` com
  `ttlCache[T]` genérico (host + containers); `ErrDockerUnavailable` → `503 docker_unavailable`;
  tabela "Containers" no Overview (estado vazio e "Docker unreachable").
- **Testado:** `sudo make check` passou (daemon Docker falso em unix socket — `stats_test.go`;
  cache TTL — `monitoring/service_test.go`; contrato HTTP — `TestContainerStats`).
- **Verificado de verdade (usuário, 2026-11-17):** container `mesquita-test-stats` com label
  `mesquita.managed=true` + `sudo make run` → apareceu na tabela Containers do Overview; Crafty e servidores Minecraft **não** apareceram (filtro por label ok); `make clean` depois.

## Fase 3 — estado (2026-11-17)

Plano: `~/.claude/plans/memory-md-fa-a-o-planejamento-elegant-parnas.md`. Decisões D19–D24.
- **Implementado:** migration `0002_servers`; `domain/server.go` (validações, status);
  `docker.Runtime` (`runtime.go`: pull por digest, create/start/stop/restart/kill/remove/update,
  inspect com checagem de posse) + `listManaged` compartilhado com stats; `filesystem.ServerDirs`
  (`os.Root`); `hostnet.TCPPortFree`; `application/servers` (create com rollback, lifecycle,
  update, delete, counts); rotas `/api/v1/servers…` (`docs/API.md`); `servers` no `/dashboard`;
  `MESQUITA_GAME_PORTS`; frontend `pages/servers.js` + `dialog.js` (`<dialog>` nativo);
  contagem no Overview.
- **Testes escritos:** `docker/runtime_test.go` (daemon falso), `sqlite/servers_test.go`,
  `filesystem/servers_test.go` (traversal, symlink), `servers/service_test.go` (rollback,
  posse, status), `http/servers_test.go` (contrato), config.
- **Testado:** `sudo make check` passou (gofmt, vet, testes, build; binário 12 MB).
- **Verificado de verdade (usuário, 2026-11-17):** criar servidor Vanilla `LATEST` → `stopped`; Start → `running` (depois da correção de D21; antes ficava em loop `restarting`).
- **Verificado de verdade (usuário, 2026-11-17), ciclo completo:** conectar pelo cliente, stop salvando o mundo, restart, kill, editar limites, delete com e sem arquivos; Crafty intacto.
- **Pendente:** deploy systemd com grupo `docker` (D10, precisa autorização). Página de detalhes do servidor (logs, console, arquivos) começa na Fase 4.
- **Fora do escopo:** console/logs (4), arquivos (5), backups (6), outros loaders (7).

## Fase 4 — estado (2026-11-17)

Plano: `~/.claude/plans/fa-a-o-planejamento-da-encapsulated-church.md`. Decisões D25–D29.
- **Implementado:** adapter `infrastructure/websocket` (gorilla, Origin estrito, limites);
  `docker/console.go` (`FollowLogs` com demux, `SendInput` por attach, `WatchEvents`) +
  `Client.stream`; `application/console` (feeds compartilhados, histórico, status por eventos,
  `SendCommand` com auditoria `server.console_command`); `presentation/websocket` (Hub:
  registry, limites, filas, revalidação de sessão, roteamento, shutdown); rota `GET /api/v1/ws`;
  `main.go` (Run + Close do console, `hub.Shutdown` após o `http.Server`); `statusRecorder`
  com `Hijack`; frontend `ws.js`, `server-actions.js`, página `#/servers/{id}`
  (`pages/server.js`), roteador com parâmetro; `docs/WEBSOCKET.md`, `docs/API.md`.
- **Testado:** `sudo make check` passou (gofmt, vet, testes, build; binário 12,7 MB).
- **Verificado de verdade (usuário, 2026-11-17):** comandos pelo console chegam ao jogo (D25);
  RSS ~34 MB com console aberto e servidor ativo. Input fica desabilitado com o servidor parado
  (comportamento esperado; confundiu no primeiro teste).
- **Verificado de verdade (usuário, 2026-11-17), restante:** status ao vivo sem reload
  (Stop/Start); reconexão sozinha após reiniciar o painel, com histórico; logout em outra aba
  fecha o console (4401); dois consoles simultâneos; Crafty intacto.
- **Fora do escopo:** métricas por WebSocket (seguem por polling, D18); arquivos (5), backups (6).

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
