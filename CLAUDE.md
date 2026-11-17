# CLAUDE.md — Mesquita Panel

Painel self-hosted e leve para gerenciar game servers (Minecraft primeiro) em containers Docker.
Backend Go, frontend HTML + Vanilla JS (ES modules, Web Components) + Tailwind CSS v4, tudo
embutido num único binário Linux amd64. Alternativa ao Crafty Controller / Pterodactyl.

- **Fonte de verdade:** [`docs/SPEC.md`](docs/SPEC.md) (spec v1.3). Não duplicar aqui.
- **Decisões e estado:** [`MEMORY.md`](MEMORY.md). Ler no início de cada sessão. Atualizar ao
  tomar decisão arquitetural, descobrir fato do host ou concluir/avançar uma fase.

Conversa com o usuário em **português**. Código, identificadores, comentários e docs técnicas
(`docs/*.md`) em **inglês**.

---

## ⚠️ Esta máquina É a produção (`mesquita-server`)

O repositório vive no próprio servidor que roda Crafty e os servidores Minecraft reais.

**Proibido sem autorização explícita do usuário (na conversa atual):**

- Parar, reiniciar, recriar ou modificar o container do Crafty ou qualquer container Minecraft.
- Ler/alterar/mover/apagar dados em `/home/bruno-mesquita/docker/`.
- `docker network *`, `docker system prune`, `docker volume rm`, `docker rm`/`stop`/`kill`
  em qualquer container não criado pelo painel.
- Instalar pacotes (apt, snap, Go, Node), editar `/etc`, units systemd, firewall, Tailscale,
  grupos de usuário (inclusive adicionar alguém ao grupo `docker`).
- Bind em portas já usadas (ver MEMORY.md) ou em `0.0.0.0` por padrão.

**Permitido:** container efêmero de build/test (`docker run --rm golang:<tag>`), e
containers/imagens que o próprio painel criou com label `mesquita.managed=true`.
Testes de integração Docker usam label `mesquita.managed=true` **e** nome com prefixo
`mesquita-test-`, e limpam só o que criaram.

Nota: o usuário `bruno-mesquita` **não tem acesso** a `/var/run/docker.sock`. Se um comando
Docker falhar por permissão, pedir ao usuário (ex.: rodar com `! sudo make test`); não
contornar.

---

## Comandos

Go **não** está instalado no host. Tudo roda em container pinado via `Makefile`:

| Alvo | O que faz |
|---|---|
| `sudo make check` | gofmt + vet + test + build num só passo (use este) |
| `make build` | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/mesquita-panel ./cmd/panel` dentro de `golang:<tag exata>` |
| `make test` | `go test ./...` (com `-race` exige CGO; rodar race em alvo separado se necessário) |
| `make vet` / `make fmt` | `go vet ./...` / `gofmt -l -w .` |
| `make css` | Tailwind v4 **standalone CLI** (binário pinado + SHA-256 em `tools/`, gitignored) gera `web/assets/css/app.css`, que é **commitado** |
| `make run` | Roda o binário em `127.0.0.1:8090` com `MESQUITA_DATA_DIR` temporário |

Docker roda via `sudo`, que **não funciona dentro do Claude Code** (nem com `!`: sem TTY).
O usuário roda build/test num terminal próprio, gravando a saída em `.cache/last-run.log`;
leia esse arquivo em vez de pedir para colar a saída. O container roda como o usuário do host e guarda
caches em `.cache/` (gitignored). Versões exatas de Go e Tailwind ficam no topo do Makefile
e no MEMORY.md. `make` pode não estar instalado — ver pendências no MEMORY.md.

---

## Stack (fixa — não trocar sem registrar decisão no MEMORY.md)

- HTTP: `net/http` + `http.ServeMux` com padrões Go 1.22+ (`POST /api/v1/servers/{id}/start`). Sem router externo.
- SQLite: `modernc.org/sqlite` (pure Go, CGO desligado). WAL + `busy_timeout`. Migrations SQL versionadas e embutidas.
- Senhas: `golang.org/x/crypto/argon2` (Argon2id).
- WebSocket: `github.com/gorilla/websocket`, **somente** atrás do adapter em `internal/infrastructure/websocket`. Nunca implementar framing/handshake.
- Docker: Engine API via `net/http` sobre unix socket. **Sem** SDK oficial do Docker.
- Logs: `log/slog`.
- Frontend: HTML, ES modules, Web Components, Fetch, WebSocket nativos, SVG inline, Tailwind v4 (`@theme` para tokens).
- **Proibido:** React, Vue, Angular, Svelte, shadcn/ui, Bootstrap, jQuery, Node/npm em runtime, Vite, Webpack, PostCSS, CDN público. Tudo servido via `embed`, funcionando offline.

Antes de adicionar qualquer dependência Go nova: justificar (stdlib não resolve?), preferir pure-Go, registrar no MEMORY.md.

---

## Arquitetura

Clean Architecture por **camada** (não por feature — ver MEMORY.md D8):

```
cmd/panel/main.go                # único composition root: lê config, monta adapters, injeta
internal/domain/                 # entidades, value objects, regras, erros. Zero imports de infra.
internal/application/<feature>/  # casos de uso; define as interfaces (ports) que consome
internal/infrastructure/         # docker, sqlite, filesystem, metrics, websocket — implementa ports
internal/presentation/http/      # handlers, rotas, middlewares, validação, JSON
internal/presentation/websocket/ # transporte WS, mensagens versionadas
migrations/                      # NNNN_descricao.sql, embutidas
web/                             # frontend embutido
deployments/systemd/
docs/
```

Regra de dependência: `domain` ← `application` ← `infrastructure` / `presentation`.
`presentation` nunca chama `infrastructure` diretamente; passa por `application`.

### SOLID sem cerimônia

- Interfaces pequenas, **definidas no pacote que as consome**.
- Não criar interface para algo com uma implementação e sem necessidade real de substituição ou teste.
- DI por construtor (`NewX(deps...)`). Sem estado global mutável, sem `init()` com efeitos, sem singletons.
- Nada de factories/registries/generics "para o futuro". Abstração nasce quando o segundo caso aparece (exceção: os ports exigidos pela spec — ContainerRuntime, repositórios, adapter WS).

---

## Contratos estáveis (migração futura para Rust)

São contratos públicos; mudar exige atualizar a doc correspondente **e** os testes de contrato:

- REST JSON em `/api/v1/...` → `docs/API.md`. Erro padrão: `{"error":{"code":"snake_case","message":"..."}}`.
- Mensagens WebSocket `{"version":1,"type":"...","channel":"...","payload":{...}}` → `docs/WEBSOCKET.md`.
- Schema SQLite → migrations são append-only; nunca editar migration já aplicada.
- Nunca vazar tipos/formatos específicos de Go (ex.: `time.Duration` cru, nomes de erro Go) nos contratos. Datas em RFC 3339 UTC; IDs como string.

---

## Checklist de segurança (todo handler / feature)

- Autenticação + autorização; CSRF (header de token) em todo método mutante.
- Validar input e limitar tamanho de body (`http.MaxBytesReader`), uploads e mensagens WS.
- Filesystem: usar `os.Root` (Go 1.24+) para confinar ao diretório do servidor; bloquear traversal e symlink escape; testar.
- Docker: allowlist de operações; só agir em containers com `mesquita.managed=true` **e** `mesquita.server_id` conhecido no banco. Nunca repassar comando shell arbitrário ao host.
- Login com rate limit; sem senha padrão; endpoint de setup inacessível depois de concluído.
- Cookies `HttpOnly`, `SameSite=Strict`, `Secure` quando servido sob TLS.
- Auditar ações administrativas (audit_log).
- Headers de segurança (CSP sem `unsafe-inline` para script, `X-Content-Type-Options`, `Referrer-Policy`, `frame-ancestors 'none'`).

## Eficiência

Goroutines com dono e ciclo de vida claros (cancelamento por `context`); canais e filas
**bounded**; streaming para arquivos/backups/logs (nunca carregar diretório inteiro em RAM);
write deadlines no WS; métricas com cache curto; shutdown gracioso. Medir consumo real em vez
de prometer números.

---

## Fluxo de trabalho

1. Uma fase por vez (spec §17). Fase atual e próximos passos: MEMORY.md.
2. Antes de editar: ler o código envolvido e o MEMORY.md; reusar o que existe.
3. Toda feature vem com testes (unit + contrato HTTP/WS quando tocar API). `make test` e `make build` devem passar antes de declarar pronto.
4. Ao terminar: atualizar MEMORY.md (implementado / testado / planejado) e a doc em `docs/` afetada.
5. Commits/pushes só quando o usuário pedir.
