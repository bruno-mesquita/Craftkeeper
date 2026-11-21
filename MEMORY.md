# MEMORY.md — Craftkeeper

Registro vivo de fatos do host, decisões arquiteturais e estado do projeto.
Atualizar sempre que algo aqui mudar. Spec completa: [`docs/SPEC.md`](docs/SPEC.md).

Última atualização: **2026-11-21**

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
- **D2 · Porta padrão 8090, bind em `127.0.0.1` e `100.126.54.17`** — 8080 já está ocupada; painel não deve ser público. — `LISTEN` aceita lista de endereços; nunca `0.0.0.0` por padrão.
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
- **D19 · Imagem de jogo `itzg/minecraft-server:2026.11.0` (Java 25) pinada por digest** (escolha do usuário, 2026-11-17) — baixa o jar sozinha via `TYPE`/`VERSION`; a Fase 7 vira ampliar a allowlist de loaders. Digest do índice multi-arch `sha256:8d5080d8dcd0a2629c2e4f248678373a337a40537ad6ef2857c7dbcda6e3eadd` (Docker Hub, verificado 2026-11-17). Java 25 e não 21: o Minecraft atual exige 25. Fase 7 trocou por uma imagem por Java (D45). Atualizar = trocar `gameImages` em `docker/runtime.go`.
- **D20 · Portas de jogo `25601–25700` (`GAME_PORTS`), bind em `0.0.0.0`** (escolha do usuário) — Crafty usa 25500–25600. Porta = pedida ou primeira livre (não usada no banco + `net.Listen` passa); UNIQUE no banco; `port_in_use` no start cobre a corrida.
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
- **D30 · File manager via `os.Root` na pasta do servidor** (Go 1.25+: `MkdirAll`, `RemoveAll`, `Rename`, `Lchown`, `Chmod`, `FS`). `domain.CleanServerPath` só dá erros claros; a garantia é o `os.Root` (erro "path escapes from parent" → `422`). Symlinks listados, nunca seguidos para fora; não editáveis; zip e extração os ignoram. O adaptador recebe caminhos já limpos (`""` = raiz).
- **D31 · Arquivos criados pelo painel com uid:gid do container** (D21) — `Lchown` após criar; arquivos 0640, pastas 0750; edição preserva o modo original.
- **D32 · Escritas atômicas** — upload, edição e extração gravam `.mesquita-tmp-*` na mesma pasta, `fsync`, `rename`; falha remove o temporário. Overwrite só explícito (`409 file_exists`); corrida `Lstat`→`Rename` aceita.
- **D33 · Limites fixos** — upload ≤ 10 GiB/requisição (era 2; subido em 2026-11-17 para mundos de ~6 GB) (multipart em streaming); editor ≤ 1 MiB, UTF-8 sem NUL; listagem ≤ 10 000; extração ≤ 100 000 entradas e ≤ 16 GiB realmente (era 8) descomprimidos; entrada com `..`/absoluta rejeita o zip inteiro; conflitos checados antes de escrever. Nomes sem a flag UTF-8 do zip são decodificados como CP437 (`cp437.go`; glifos em 0x01–0x1F, ex.: 0x15 = `§` dos nomes de mundo do Minecraft — achado no teste real de 2026-11-17).
- **D34 · Deadlines por progresso** — download/zip/upload estendem o deadline a cada chunk (60 s parado derruba); erro depois de começar o stream aborta a conexão (`http.ErrAbortHandler`) em vez de entregar arquivo truncado.
- **D35 · Versão para edição concorrente** = inode + mtime ns + tamanho (o mtime do kernel é grosseiro; cada escrita atômica troca o inode). `PUT content` com versão diferente → `409 file_changed`. Escrita no lugar com mesmo tamanho no mesmo tique não é detectável (raro; aceito).
- **D47 · Container órfão removível** — `DELETE /api/v1/system/containers/{id}` (`servers.Service.DeleteOrphanContainer`): só container com `mesquita.managed=true` listado pelo runtime cujo `server_id` não existe no banco; server registrado → `409 container_not_managed`. Botão "Remove" na linha órfã do Overview. Audit `container.delete`.
- **D48 · Criação de servidor assíncrona** (pedido do usuário, 2026-11-18) — o pull da imagem travava o diálogo por minutos. — `POST /servers` valida, resolve o loader, escolhe a porta e grava a linha (sem `container_id`) e responde `202 creating`; um goroutine do `servers.Service` (ctx próprio, `WaitGroup`, `Close()` no shutdown) faz pull → pasta → container → `repo.Update`, um build por vez (`provisionLock`). Com `start: true` (diálogo sem modpack) inicia ao terminar; com modpack o diálogo espera o build (polling 3 s) e o instalador é quem inicia. Status `creating`/`create_failed` vêm de um mapa em memória de jobs (D22 continua: nada persistido); falha desfaz pasta/container, **mantém a linha** com `failure` (escolha do usuário) e só aceita `DELETE`; durante `creating`, ações/edição/delete → `409 server_busy`. `Recover` no start apaga linhas sem `container_id` (criações interrompidas/falhas); container criado sem registro vira órfão (D47).
- Escrita de arquivos permitida com o servidor rodando (escolha do usuário); a UI avisa.
- **D36 · Backup = zip da pasta inteira, reaproveitando a Fase 5** — `writeZip`/`extractZip` (núcleos extraídos de `zip.go`) já são endurecidos. Entradas sem prefixo; `flate.BestSpeed` (região do Minecraft já é zlib); pula symlinks e `.mesquita-tmp-*`; arquivo removido durante o walk é ignorado. Arquivos em `<data>/backups/<server_id>/<backup_id>.zip`, dono = usuário do painel, fora do file manager.
- **D37 · Criação assíncrona** — `POST` → `202 creating`; job em goroutine do `backups.Service` (ctx próprio, `WaitGroup`, semáforo global de 1 archive por vez). UI faz polling de 3 s só enquanto há `creating` e a aba está visível. `Recover` no start (antes de servir): `creating` → `failed/interrupted`, remove `.mesquita-tmp-*` e `servers/.restore-*`.
- **D38 · Backup online** (escolha do usuário) — `running`: inscreve no console, `save-off` → `save-all flush` → espera `Saved the game` com timestamp **posterior** ao envio (feed novo reenvia o tail como linhas novas!) até 60 s → zip → `save-on` sempre (defer registrado antes do `save-off`). `stopped`/`missing`: direto; se o servidor ligar durante o zip, falha `server_busy` e o arquivo é apagado. `starting`/`restarting` → `409 server_busy`. Aviso no chat (escolha do usuário, fixo, em inglês): `tellraw @a` cinza no início e no fim (sucesso/falha), depois do `save-on`; falha ao enviar só vai para o log. Verificado com servidor real (2026-11-17).
- **D39 · Reserva de disco de 2 GiB** — host é produção. Backup exige livre − 2 GiB ≥ tamanho dos arquivos (rechecado ao sair da fila); restore descomprime no máximo livre − 2 GiB (`ErrArchiveTooLarge` vira `insufficient_storage`).
- **D40 · Restore síncrono em duas fases** — exige `stopped`/`missing`; extrai em `servers/.restore-<id>-<rand>` (uid:gid do container), reconfere o status e troca: atual → `servers/.deleted/<id>-<unix>-pre-restore` (escolha do usuário), staging → `<id>`. Ligou no meio → descarta, `409`. Cancelar a requisição descarta (nada muda). Modos viram 0640/0750.
- **D41 · Um create/restore por servidor** (`409 backup_in_progress`); delete recusa backup `creating` ou restore em andamento. `ServerDirs.Remove/Archive` cuidam também de `backups/<id>` (mesma política D23). Servidor apagado durante um job: linha some por cascade, job apaga o arquivo órfão.
- **D42 · Agendamento por intervalo + retenção** (escolha do usuário) — `interval_hours` 0–720, `retain` 0–100 (0 = todos), só backups agendados (manuais nunca são apagados sozinhos; agendados `failed` são limpos após um sucesso). `backups.Service.Run`: ticker 1 min; ocupado → tenta no próximo tick; outra recusa → grava backup agendado `failed` (aparece na lista) e espera um intervalo; mantém a cadência (`next += interval`), após downtime roda uma vez e segue de agora. Mudar só `retain` mantém o próximo horário.
- **D43 · Catálogo de versões pelas fontes oficiais, HTTPS fixo** (escolha do usuário, 2026-11-17) — Mojang `piston-meta`, Fabric `meta.fabricmc.net`, Forge `maven.minecraftforge.net` + `promotions_slim.json`, NeoForge `maven.neoforged.net`. URLs constantes em `gameversions.Official` (a spec fala em "configurable": desvio consciente, são fixas). "Verified" = TLS com CAs do host + versões exatas pinadas + imagem por digest (o itzg baixa das mesmas fontes). Timeout 15 s, ≤ 8 MiB, User-Agent `craftkeeper`; cache 1 h por fonte (`application/ttlcache`, extraído do monitoring); fonte fora sem cache → `503 installer_unavailable`. Tudo o que sai das fontes passa pelas regex do domain antes de virar env do container.
- **D44 · Versões pinadas para modded** — Fabric/Forge/NeoForge exigem MC concreto (sem `LATEST`) e `loader_version` existente no catálogo; omitido = recomendado, gravado (migration `0004`). Sem isso o itzg re-resolve a versão a cada start e atualiza o loader sob os mods. Vanilla mantém `LATEST` e não depende da rede. Recomendado: Fabric = stable mais novo; Forge = promo `recommended` → `latest` → mais novo; NeoForge = mais novo sem `-beta`. NeoForge→MC: `21.1.x`→`1.21.1`, `21.0.x`→`1.21`; ≥ 26: `26.1.2.127`→`26.1.2`, `26.3.0.110`→`26.3`. Forge: entradas `<mc>-<build>` com sufixo extra (1.7.10, 1.10, …) são ignoradas. Trocar loader/versão de servidor existente e modpacks: fora do escopo.
- **D45 · Imagem Java por versão do MC** (escolha do usuário: automático, sem override) — `≤1.16.5`→java8, `1.17–1.20.4`→java17, `1.20.5–1.21.x`→java21, `26.x`/`LATEST`→java25. Tags `2026.11.0-java8/17/21` + `2026.11.0` (= `-java25`), digests lidos do Docker Hub em 2026-11-17, em `docker.gameImages` (allowlist; java desconhecido nunca chega ao daemon). Pull só quando usada. Derivada da versão gravada, não persistida. Resolve a pendência de versões antigas da D19.
- **D46 · Identidade visual "deepslate + lanterna"** (2026-11-17) — sair do visual shadcn (zinc + azul). Tokens em `web/styles/app.css`: fundo deepslate `#0f1316`, texto pergaminho `#ebe6d6`, acento âmbar `#f0b040`, sucesso musgo, perigo redstone. Fontes vendorizadas em `web/assets/fonts/` (Bricolage Grotesque + JetBrains Mono, SIL OFL, variáveis, só latin; CSP `font-src 'self'`). Medidor segmentado (`.meter`, máscara de blocos de 6px), badges com LED pulsante, logo "M" pixelado. Sem mudança de API/JS de comportamento.
- **D54 · Crash loop por `RestartCount`, gravado no banco** (escolhas do usuário, 2026-11-18) — o `unless-stopped` deixava um servidor com mod quebrado reiniciando para sempre (Fase 7). — `servers.Service.Run` segue um 2º stream de Docker events; em cada `start` faz `inspect` e conta aumentos do `RestartCount` (o Docker só o incrementa em restarts da política). 3 em 10 min (constantes) → `MarkCrashed` (migration `0005`: `crashed_at`, `crash_restarts`) → audit `server.crash_loop` sem usuário → `Stop` em goroutine do `wg`. Status `crashed` é derivado (container parado + registro; D22 vale para o status), age como `stopped` em todo lugar; Start/Restart pelo painel limpam o registro. `Update` do repo não toca as colunas (só `MarkCrashed`/`ClearCrash`). UI: badge, aviso e dialog com o crash report mais novo via API de arquivos.
- **D55 · HTTPS por `tailscale serve` + `SECURE_COOKIE`** (escolha do usuário, 2026-11-18) — o painel não termina TLS; o Tailscale renova o certificado. Produção escuta só em `127.0.0.1:8090` (sem HTTP paralelo pelo IP do Tailscale). Cookie `Secure` = `r.TLS != nil || SecureCookie`; cabeçalhos de proxy nunca são confiáveis, então atrás do serve todo cliente aparece como `127.0.0.1` (audit e limite de login por IP — aceito, um admin).
- **D56 · Variáveis de ambiente sem prefixo** (pedido do usuário, 2026-11-18) — `LISTEN`, `DATA_DIR`, `DOCKER_SOCKET`, `LOG_LEVEL`, `GAME_PORTS`, `SECURE_COOKIE` (eram `MESQUITA_*`). — Em produção vêm só do `EnvironmentFile` do systemd, isolado; no `make run`/shell, uma variável genérica herdada (ex.: `LOG_LEVEL` de outra ferramenta) passa a valer para o painel.
- **D57 · `audit_log.server_id`** (Fase 9, 2026-11-18) — o target usava o nome (muda, pode conter `": "`). — Migration `0006_audit_server` (coluna + índice `(server_id, id)`, sem FK: histórico de servidor apagado fica; sem backfill, eventos antigos só na página global). Leitura com `LEFT JOIN users`/`servers` (username, nome atual ou nulo). `GET /api/v1/audit` com filtros `server_id`/`action`/`failed`/`before`/`limit` (keyset, ≤ 200); `application/activity` valida. `Recent` removido (dashboard usa `List`).
- **D58 · `server.console_command` com target = só o comando; comandos do painel não são auditados como console** — o ↑ não pode trazer `save-off` de volta e cada intenção = uma linha no audit. — `console.Service.Send` (sem audit) para backups/players; `SendCommand` (auditado) só para o WS. Histórico ↑/↓ = `GET /audit?server_id&action=server.console_command&limit=200` (sucesso + com usuário, dedup, ≤ 50).
- **D59 · Logs antigos lidos no navegador** (mesmo raciocínio da D49/D52) — lista `logs/` e baixa via API de arquivos; `.gz` por `DecompressionStream`, streaming, mantém as últimas 20 000 linhas e para em 64 Mi caracteres. Lista relida a cada mudança de status (rotação no start). Busca = filtro case-insensitive por substring (live e arquivo).
- **D60 · Troca de versão recria o container** (Fase 11, escolhas do usuário 2026-11-18: downgrade permitido com aviso; qualquer troca de loader) — env `TYPE`/`VERSION`/`*_VERSION` é imutável. `servers.ChangeVersion`: só parado/crashed; marca o servidor ocupado (`replacing`, `checkBuilt` → `409 server_busy`); `Resolve` + `EnsureImage`; renomeia o antigo para `<nome>-replaced` (`docker.RenameReplaced`, nova operação da allowlist com checagem de posse), cria o novo (mesma porta/limites/pasta via `ServerDirs.Path`), `repo.Update` (agora grava loader/version/loader_version, sem migration), reconfere que o antigo segue parado e o remove; qualquer falha desfaz (linha antiga, remove o novo, nome de volta). Limpa crash record. `Recover` remove, best effort, um 2º container de servidor registrado (troca interrompida). Servidor com modpack configurado → `409 modpack_server`.
- **D61 · Backup automático antes de toda operação destrutiva** (escolha do usuário: sempre) — `backups.Protect(ctx, id, actor, change)`: backup **síncrono** (kind `manual`, nunca podado; sem migration no CHECK de `kind`) reaproveitando `complete` (núcleo extraído do `run`), servidor segurado (`opProtect`) até o fim da mudança — sem backup/restore intercalado. Falha → `500 backup_failed`, nada muda. Request cancelada → backup fica, mudança não roda. `wg` cobre só o backup (Close não espera a mudança). Start não é bloqueado: a mudança reconfere `stopped` (padrão D40).
- **D62 · `application/maintenance`** — versão, reset e import de mundo: valida o que é barato (input, `level-name`, zip) → `Protect` → reconfere parado → aplica. Mundo = `level-name` do `server.properties` (default `world`, `domain.LevelName` via `CleanServerPath`). Reset apaga a pasta e, com `seed`, grava `level-seed` (com versão, D35). Import: zip já enviado pelo upload existente, extraído (limites D33) em `.mesquita-tmp-world-*` (ignorado pelo backup; `CleanupStale` agora limpa `.mesquita-tmp-*` no topo de cada pasta de servidor), `level.dat` na raiz ou numa única pasta de topo (`__MACOSX` ignorado), senão `422 invalid_world` sem backup; zip apagado depois de extraído. Export = download da pasta pela API de arquivos (só frontend). Layout Bukkit fora do escopo. Requests síncronas, deadline 45 min.
- **D63 · Operadores pelo padrão da whitelist** — `op`/`deop` pelo console rodando; parado edita `ops.json` (`level` = `op-permission-level`, default 4; `bypassesPlayerLimit=false`). Audit `player.op`/`player.deop`.
- **D64 · Mods sem estado no banco: identificados pelo hash** (Fase 12, 2026-11-19) — `mods/*.jar` e `*.jar.disabled` são o estado; SHA-512 em cache de memória (servidor + nome, invalidado por tamanho/mtime, podado a cada listagem); Modrinth identifica (`POST /v2/version_files`) e acha updates (`/version_files/update` com loader + MC do servidor; só oferece se publicado depois). Jar desconhecido = "manual". Sem migration; uploads manuais e restores ficam coerentes.
- **D65 · Cliente Modrinth fixo + download verificado** — `infrastructure/modrinth` (`URLs`/`Official`, User-Agent `craftkeeper`, JSON ≤ 8 MiB, timeout 15 s; download sem timeout total, só o ctx da requisição + timeouts de handshake/cabeçalho). Só baixa de `https://cdn.modrinth.com/` (redirect para fora recusado), ≤ 256 MiB, reader confere tamanho + SHA-512 e falha **antes** do EOF → `ServerFiles.Save` (D32) descarta o temporário. Reserva de 2 GiB (`ServerFiles.FreeBytes`). Nome gravado sanitizado (`domain.SafeModFileName`). Fonte fora/hash errado → `503 modrinth_unavailable`; listagem degrada (`catalog_unavailable`).
- **D66 · Escopo e regras dos mods** (escolhas do usuário, 2026-11-18) — só modded sem modpack (`409 mods_unsupported`/`modpack_server`; `domain.ModpackConfigFile` consolidado). Mudanças permitidas com o servidor rodando (UI mostra "Restart"). Versão = release mais nova compatível (senão a mais nova). Deps `required` em largura, puladas se o projeto já está instalado (ativo ou não), ≤ 32 arquivos por operação; `incompatible` em qualquer sentido → `409 mod_incompatible`. Install tudo-ou-nada (remove o que escreveu). Update: `backups.Protect` opcional (campo `backup`, padrão `false` na API; checkbox marcado por padrão na UI — pedido do usuário, 2026-11-19; install/remove nunca fazem backup) → baixa → remove o antigo; desativado continua desativado. Ativar/desativar = renomear `.jar` ↔ `.jar.disabled`. Uma operação de mods por servidor (`409 server_busy`). Busca: `project_type:mod`, `categories:<loader>`, `versions:<mc>`, `server_side` required/optional.
- **D67 · Ícones de mods por proxy** — `GET /api/v1/mods/icons/{project_id}`: o backend resolve o `icon_url` (cache 1 h, ≤ 1024 entradas), só host do CDN, ≤ 256 KiB, PNG/JPEG/GIF/WebP por sniff (SVG recusado), `Cache-Control: private, max-age=86400`. Cliente nunca passa URL; CSP inalterada.
- **D68 · Modpack com o mesmo script para instalar e iniciar** (pedido do usuário, 2026-11-20) — `PUT /modpack` aceita `install_script == start_script`; o wrapper do container pula a etapa de instalação, grava `.mesquita-modpack-installed` e executa o script uma única vez por start (como launcher).
- **D10 · systemd com usuário dedicado `craftkeeper`** — princípio do menor privilégio. — Precisa estar no grupo `docker` (root-equivalente) a partir da Fase 3; criação do usuário e grupo exige autorização no deploy.
- **D69 · Renomeado para Craftkeeper** (escolha do usuário, 2026-11-20) — o repo/README já se chamavam Craftkeeper. Módulo `github.com/bruno-mesquita/Craftkeeper`; binário, unit, usuário, `/opt|/etc|/var/lib/craftkeeper`. Runtime: labels `craftkeeper.managed/server_id/game`, container `craftkeeper-<id>`, marcadores `domain.Modpack{Config,Installed,Supported}File` = `.craftkeeper-modpack{,-installed,-supported}`, temporários `.craftkeeper-tmp-*`, cookie `craftkeeper_session`. A migração dos containers `mesquita.*` (recriação via `servers.swapContainer`, extraído da D60) rodou uma vez e o único container legado foi migrado e verificado pelo usuário (2026-11-21); a compatibilidade foi **removida** em seguida — labels e marcadores `mesquita*` não são mais reconhecidos. `AuditServerMigrate` (`server.migrate`) fica só para as linhas antigas do audit. Decisões antigas citam os nomes `mesquita*` como histórico.
- **D70 · Deploy por script local `sudo make deploy`** (escolha do usuário, 2026-11-20) — repo público: runner self-hosted rodaria código de PR na produção; push pelo CI exigiria chave root no GitHub. — `deployments/deploy.sh` só faz upgrade (recusa sem root/unit/binário): `make check` (falha → nada muda) → stop → backup frio do SQLite + binário em `/var/backups/craftkeeper/<UTC>-<rev>/` (5 mais novos) → install → start → `/api/v1/health` por 30 s → senão rollback de binário **e** banco + `journalctl`. `SKIP_BUILD=1`, `HEALTH_URL`. Saída em `.cache/deploy.log`. Primeira instalação segue manual (INSTALL.md §1–8b, autorização).

---

## Pendências e riscos abertos

- [x] Docs do §19 completos (2026-11-17): `ARCHITECTURE.md`, `SECURITY.md`, `DEVELOPMENT.md` adicionados. Próximo: deploy systemd (§20), com autorização do usuário.
- [x] Acesso ao Docker para dev/test: sudo não funciona dentro do Claude Code (nem via `!`: sem TTY). Decisão (2026-11-17): o usuário roda os comandos num terminal próprio, com saída em `.cache/last-run.log`, que o Claude lê. Recusadas: Go em ~/.local, grupo docker.
- [x] Versões pinadas: Go `golang:1.27.4-trixie`, Tailwind CLI v4.3.5 (SHA-256 no Makefile), `modernc.org/sqlite` v1.61.1, `golang.org/x/crypto` v0.58.0.
- [x] `make` instalado. Fluxo: usuário roda `sudo make check` num terminal próprio; Claude lê `.cache/last-run.log`.
- [ ] Cookie `Secure` exige HTTPS: avaliar `tailscale serve` para TLS no acesso remoto.
- [x] Git: branch `main`, módulo `github.com/bruno-mesquita/Craftkeeper`, identidade configurada. Commits (Fases 1 e 2) **só locais** — não fazer push sem o usuário pedir (ele ainda vai instalar o `gh`).
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
| 5 | File manager | ✅ implementada, testada e verificada com servidor real (2026-11-17) | ver Fase 5 abaixo |
| 6 | Backups | ✅ implementada, testada e verificada com servidor real (2026-11-17) | ver Fase 6 abaixo |
| 7 | Game installers (Vanilla, Fabric, Forge, NeoForge; Paper/Purpur fora por escolha do usuário) | ✅ implementada, testada e verificada com servidor real (2026-11-17) | ver Fase 7 abaixo |
| 8 | Pronto para produção (crash loop, HTTPS, deploy) | 🚧 crash loop testado e verificado com servidor real; falta deploy + HTTPS (2026-11-18) | ver Fase 8 abaixo |
| 9 | Console e eventos (histórico, busca, logs antigos, Activity) | ✅ implementada, testada e verificada (2026-11-18) | ver Fase 9 abaixo |
| 10 | Tarefas agendadas + webhook Discord | ⏸️ opcional (pulada a pedido do usuário, 2026-11-18) | ver "Roadmap pós-Fase 7" abaixo |
| 11 | Trocar versão/loader, mundos, operadores | ✅ implementada, testada e verificada (2026-11-18) | ver Fase 11 abaixo |
| 12 | Mods via Modrinth | 🚧 implementada e testada (`make check` ok); falta verificação real (2026-11-19) | ver Fase 12 abaixo |
| 13–14 | Roadmap pós-Fase 7 (features) | 📋 planejadas (2026-11-18) | ver "Roadmap pós-Fase 7" abaixo |

Legenda de detalhe por feature (preencher ao implementar): **implementado** / **testado** / **planejado**.

---

## Fase 1 — verificação (2026-11-17)

- `sudo make check`: gofmt, vet, todos os testes e build passaram. Binário estático de 12 MB.
- E2E com curl em `127.0.0.1:8090`: setup com token → 201; segundo setup → 409; login → cookie HttpOnly + csrf; stats reais; dashboard; POST sem CSRF → 403; cross-site → 403; listagem de diretório → 404; logout → 204, depois `me` → 401.
- Restart: banco persiste, setup não é mais exigido, token não é impresso, login ok. SIGTERM → shutdown gracioso.
- Memória: ~53 MB RSS depois de um login (o Argon2id aloca 19 MiB, que o heap do Go retém); 12 threads. Se incomodar, avaliar `GOMEMLIMIT` / `debug.FreeOSMemory` — medir antes.
- UI no navegador: abriu e funcionou pelo Tailscale (`make run RUN_LISTEN=127.0.0.1:8090,100.126.54.17:8090`), confirmado pelo usuário em 2026-11-17. Ainda não auditado: navegação por teclado e contraste.
- Acesso: `make run` escuta em 127.0.0.1 e no IP Tailscale por padrão (`RUN_LISTEN` sobrescreve); do PC do usuário, usar Tailscale (`RUN_LISTEN`) ou túnel SSH. `localhost` no PC ≠ servidor.
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
  `GAME_PORTS`; frontend `pages/servers.js` + `dialog.js` (`<dialog>` nativo);
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

## Fase 5 — estado (2026-11-17)

Plano: `~/.claude/plans/fa-a-o-planejamento-da-encapsulated-church.md`. Decisões D30–D35.
- **Implementado:** `domain/files.go` (`CleanServerPath`, `ValidateFileName`, tipos, erros,
  auditoria `file.*`); `filesystem/files.go` (`ServerFiles`: list, stat, open, read/write text,
  save, mkdir, rename, remove) e `zip.go` (`WriteZip`, `Extract`); `application/files`;
  `presentation/http/files.go` (9 rotas, `progressWriter/Reader`, `decodeJSONLimit`);
  frontend: abas Console | Files em `pages/server.js`, `file-manager.js`, `api.upload` (XHR com
  progresso), `promptDialog`; `docs/API.md` (Files).
- **Testado:** `sudo make check` passou (binário 13,0 MB). Testes de segurança: symlink para
  `/etc`, `..`, symlink loop, arquivo fora da pasta intacto, zip slip, symlink em zip, CRC
  corrompido, contagem real de bytes; contrato HTTP completo.
- **Verificado de verdade (usuário, 2026-11-17):** roteiro completo (edição + restart, 409,
  upload, download de pasta como `.zip`, renomear/criar/apagar, `?path=../..` → 422). A
  extração falhou num zip real com `§` em nome CP437 → corrigido com `cp437.go` e
  re-verificado.

## Fase 6 — estado (2026-11-17)

Plano: `~/.claude/plans/fa-a-o-planejamento-da-crystalline-taco.md`. Decisões D36–D42.
- **Implementado:** migration `0003_backups` (`backups`, `backup_schedules`, cascade);
  `domain/backup.go` (+ erros `backup_in_progress`, `backup_not_ready`, `server_busy`, auditoria
  `backup.*`); `sqlite/backups.go`; `filesystem/zip.go` refatorado (`writeZip`, `extractZip`
  com ctx e limites) + `filesystem/backups.go` (Write, Open, Remove, Usage, Stage/Commit/
  DiscardRestore, CleanupStale); `ServerDirs` com raiz de backups; `application/backups`
  (online via console, fila, retenção, agendador, Recover/Close); `presentation/http/backups.go`
  (7 rotas); `main.go` (Recover antes de servir; Close antes do console); frontend
  `backup-manager.js` + aba Backups em `pages/server.js`; `make css`; `docs/API.md`,
  `docs/CONFIGURATION.md`, README. Mensagem de `server_running` virou genérica.
- **Testado:** `sudo make check` passou (gofmt, vet, testes, build; binário 13,2 MB). Testes: `filesystem/backups_test.go`,
  `sqlite/backups_test.go`, `application/backups/service_test.go` (sequência save-off/flush/
  save-on, linha antiga ignorada, timeout, falha com save-on, exclusão, start durante backup,
  Close → interrupted, restore + corrida, retenção, agendador, Recover), `http/backups_test.go`
  (contrato completo), `ServerDirs` com backups.
- **Risco nos testes HTTP:** usam o disco real do container de teste — precisam de > 2 GiB
  livres no `/tmp` dele (reserva D39).
- **Verificado de verdade (usuário, 2026-11-17):** roteiro real completo funcionou (backup online com o console
  mostrando save-off/Saved the game/save-on, download, restore, Start durante restore → 409,
  agendamento, apagar servidor com/sem arquivos). Ainda não medido: RSS/CPU durante backup.

## Fase 7 — estado (2026-11-17)

Plano: `~/.claude/plans/a-pr-xima-a-reactive-minsky.md`. Decisões D43–D45.
- **Implementado:** `domain/installer.go` (loaders, `ValidLoaderVersion`, `CompareVersions`,
  `JavaFor`, `LoaderVersions`) + `Server.LoaderVersion`, `ContainerSpec.Java`,
  `ErrInstallerUnavailable`; migration `0004_loader_version`; `infrastructure/gameversions`
  (4 fontes, JSON/XML); `application/ttlcache` (movido do monitoring); `application/installers`
  (listas + `Resolve`); `servers.Create` com loader/Java e env `FABRIC_LOADER_VERSION` /
  `FORGE_VERSION` / `NEOFORGE_VERSION`; `docker.Runtime` com imagem por Java; rotas
  `GET /api/v1/installers/{loader}/versions[/{minecraft}]`; `POST /servers` com `loader` e
  `loader_version`; UI: selects tipo → versão MC → versão do loader (vanilla cai para texto se a
  fonte falhar), rótulo "Forge 1.20.1 · 47.3.0" na lista e na página; docs API/CONFIGURATION/README.
- **Testado:** `sudo make check` passou (2026-11-17). Testes: domain, `gameversions` (httptest com fixtures reais
  reduzidas, falhas), `installers`, `servers` (modded, Java, env, 422, fonte fora), `docker`
  (imagem por Java, java desconhecido), `sqlite` (round-trip + migration sobre banco antigo),
  HTTP (contrato installers + create modded).
- **Verificado de verdade (usuário, 2026-11-17):** roteiro real feito. Log do Fabric 1.21.11
  conferido: imagem `2026.11.0-java21`, 1º start instala loader 0.20.1; restarts seguintes
  mostram "Fabric launcher for minecraft 1.21.11 loader 0.20.1 is already available" (sem
  reinstalar nem trocar versão); JEI sem fabric-api → falha de resolução de mods → loop de
  restart (`unless-stopped`, comportamento já existente); com fabric-api 0.141.6 sobe com
  43 mods e o jogador conecta.

## Aba Players — whitelist/banlist (2026-11-17)

Plano: `~/.claude/plans/na-tela-de-detalhes-glittery-panda.md`.
- **Decisão:** rodando (running/starting) → mudanças via console stdin (o jogo dono dos
  arquivos resolve UUID e persiste); parado → edita `whitelist.json` / `banned-*.json` /
  `server.properties` pelo adapter `os.Root`. Parado + online-mode → UUID consultado na
  Mojang (D50; antes era 422). Leitura sempre dos arquivos. Switch:
  `whitelist on|off` ou `white-list`+`enforce-whitelist`.
- **Implementado:** `domain/players.go`, `application/players`, `presentation/http/players.go`
  (7 rotas, `docs/API.md` "Players"), `web/assets/js/player-manager.js` + aba em `pages/server.js`,
  auditoria `whitelist.*`, `player.*`, `ip.*`.
- **Testado:** pendente — rodar `sudo make check` (`players_test.go`, `domain/players_test.go`).
  Não verificado contra Minecraft real (comandos via console, formato dos JSONs).

## Whitelist parado (online-mode) + avatares (2026-11-18)

Plano: `~/.claude/plans/preciso-que-seja-possivel-twinkling-wadler.md`.
- **D50 · Lookup de conta na Mojang** (escolha do usuário) — servidor parado em online-mode:
  `infrastructure/mojang` busca `api.mojang.com/users/profiles/minecraft/<nome>` e grava UUID +
  grafia da Mojang em `whitelist.json`/`banned-players.json` (port `players.Accounts`). Nome
  inexistente → `422` (campo `name`); Mojang fora/429 → `503 account_lookup_unavailable`.
  Offline-mode continua com `OfflineUUID` sem rede. Hosts fixos, timeout 15 s, User-Agent
  `craftkeeper`, sem cache de lookup.
- **D51 · Avatar por proxy** — `GET /api/v1/players/{uuid}/avatar` (auth): sessionserver →
  textures (só aceita host `textures.minecraft.net` + hash hex, refeito em HTTPS; Mojang
  devolve `http://`), skin PNG ≤ 64 KiB 64×64/64×32, rosto 8×8 + chapéu (chapéu 100% opaco em
  skin legada é ignorado), stdlib `image/png`/`image/draw`. Browser guarda 1 dia
  (`Cache-Control: private, max-age=86400`); sem cache no servidor. UUID v3 (offline) → 404
  sem rede. CSP `img-src 'self'` mantida. UI: `<img>` pixelado na whitelist e banidos,
  fallback com a inicial.
- **Implementado:** domain (`ParseUUID`, `IsOfflineUUID`, erros), `infrastructure/mojang`,
  `players.Service.resolve/Avatar`, rota + mapeamento 503, `player-manager.js`, `docs/API.md`.
- **Testado:** formato real conferido via curl (conta `Master_On99`, 2026-11-18: profile,
  textures com URL `http://`, skin 64×64, 404 p/ nome inexistente). `sudo make check` passou
  (2026-11-18: gofmt, vet, testes incl. `mojang`, build). Validado pelo usuário no painel:
  adicionou `Master_On99` com servidor parado e o avatar apareceu.

## Aba Properties — `server.properties` por formulário (2026-11-18)

Plano: `~/.claude/plans/preciso-agora-que-tudo-cuddly-widget.md`.
- **D49 · Form só no frontend, sobre a API de arquivos** — `GET/PUT /files/content` já dá
  escrita atômica, `os.Root`, versão (409) e auditoria; um endpoint próprio não acrescentaria
  segurança (o arquivo cru segue editável em Files). Sem mudança de API/backend.
- **Implementado:** `web/assets/js/properties-manager.js` + aba "Properties" em `pages/server.js`.
  Parser/serializer Java Properties (desescapa `\uXXXX`, `\:`…; grava não-ASCII como `\uXXXX`);
  só reescreve linhas de chaves alteradas (comentários/ordem/CRLF preservados; arquivo intocado
  sai idêntico). Schema com rótulo/descrição/tipo por grupo; chaves desconhecidas em "Other"
  (texto); `server-port` e RCON somente leitura (container expõe 25565; `ENABLE_RCON=false`).
  Chave ausente mostra o default e só é gravada se mudar. MOTD com botões de cor/estilo `§` e
  preview. Busca, "Undo" por campo, barra de salvar; rodando → "Save and restart". 409 →
  reaplica as edições sobre a versão nova após confirmação. Arquivo inexistente: relê antes de
  gravar para não sobrescrever um arquivo que o jogo acabou de criar.
- **Testado:** round-trip do parser/serializer com node (local, ad hoc); `sudo make check` passou.
- **Verificado de verdade (usuário, 2026-11-18):** roteiro manual do plano no navegador ok.

## Modpacks — implementação inicial

- ZIP é enviado no diálogo "New server" (a aba Modpack da tela do servidor foi removida em
  2026-11-18 a pedido do usuário) usando o upload e a extração segura já existentes; colisões
  não são sobrescritas. A UI lista `.sh` regulares e exige seleção de instalador e inicializador.
- Seleção e estado ficam em arquivos `.mesquita-modpack*` na pasta do servidor; a listagem de
  arquivos oculta esses metadados. Backups preservam estado e configuração.
- Containers criados agora mantêm o launcher itzg como fallback. Com configuração de modpack,
  o primeiro start executa o instalador como usuário sem privilégios e, em caso de sucesso,
  inicia o script do servidor; os próximos starts executam apenas o script de inicialização.
  Saída passa pelo console Docker já existente. Se o instalador falha, o container fica ocioso
  para não entrar em loop de restart; parar e iniciar novamente tenta a instalação outra vez.
- O ZIP também pode ser escolhido no diálogo "New server": o frontend cria o servidor
  (`POST /servers`) e então reusa `mountModpackManager` (`initialFile`) no mesmo diálogo para
  upload, extração, escolha de scripts e instalação. Sem mudança de API/backend; se o import
  falha, o servidor já existe e o retry só é possível no próprio diálogo (fechar = servidor sem
  modpack). Endpoints `/servers/{id}/modpack*` continuam (usados pelo diálogo).
- Containers criados antes deste wrapper continuam sem alteração e retornam `supported=false`.
- **Verificação pendente:** não executada nesta implementação; falta validar compilation/contratos
  e um ZIP de server pack real no container.

## Roadmap pós-Fase 7 (definido com o usuário em 2026-11-18)

Plano: `~/.claude/plans/vamos-fazer-um-novo-ticklish-quail.md`. Objetivo: **substituir o Crafty**,
depois features. Multi-usuário fora por ora. Cada fase ganha plan mode próprio ao começar.
**Importar servidores do Crafty foi descartado** pelo usuário (2026-11-18); as fases seguintes
subiram um número.

| Fase | Escopo | Status | Autorização |
|---|---|---|---|
| 8 | 🚧 Produção: detecção de crash/restart loop (Docker events, status `crashed` derivado, último `crash-reports/`); validar Players e modpack com MC real; deploy systemd (D10) + TLS `tailscale serve` | planejada | sudo, grupo docker, systemd |
| 9 | Console e eventos: histórico de comandos, busca, `logs/*.log.gz`; página Activity + aba por servidor | ✅ concluída | — |
| 10 | Tarefas agendadas (restart/comando/anúncio; generaliza D42) + webhook Discord (crash, start/stop, backup falho, join/leave via SLP) | ⏸️ opcional (pulada a pedido do usuário, 2026-11-18) | — |
| 11 | Trocar versão/loader com backup automático; mundos (reset, seed, import/export) + `ops.json` | ✅ concluída | — |
| 12 | Mods via Modrinth (busca por loader/MC, SHA-512, updates, dependências) | 🚧 implementada | — |
| 13 | Histórico de métricas (revisa D18; 1 min, 24–48 h, SVG) + página global Backups | planejada | — |
| 14 | Clonar servidor / templates + status público (ponto de exposição a decidir) | planejada | possivelmente exposição pública |

## Fase 8 — estado (2026-11-18)

Plano: `~/.claude/plans/vamos-fazer-um-novo-ticklish-quail.md`. Decisões D54–D55.
- **Implementado:** `domain` (`StatusCrashed`, `StatusOf`, `Server.CrashedAt/CrashRestarts`,
  `ContainerState.RestartCount`, `AuditServerCrashLoop`); migration `0005_server_crash`;
  `sqlite.Servers.MarkCrashed/ClearCrash`; `docker.Runtime.Inspect` lê `RestartCount`;
  `servers/crashguard.go` (`Run`, `observeStart`, `countRestart`) + `startOp` em Start/Restart;
  `crashed` = parado em players/backups/Counts; `main.go` (goroutine do guard, termina antes do
  `Close`); DTO `crash`; `SECURE_COOKIE`; frontend (`STATUS.crashed`, `STOPPED`, aviso +
  dialog do crash report em `pages/server.js`, Resources editável); `make css`; docs API,
  CONFIGURATION, INSTALL (seção 8b), `config.env.example`.
- **Testes escritos:** `servers/crashguard_test.go`, `sqlite/servers_test.go` (Mark/Clear, Update
  não apaga), `docker/runtime_test.go` (`RestartCount`), `config` (bool), `http/internal_test.go`
  (cookie Secure, `crash` DTO), contrato `crash: null`. JS: `node --check`.
- **Testado:** `sudo make check` passou (2026-11-18; binário 13,8 MB). O 1º run pegou um bug real:
  `List` não derivava `crashed` (lista/Overview mostravam `stopped`) — corrigido.
- **Verificado de verdade (usuário, 2026-11-18):** crash loop com servidor real funcionou.
- **Pendente:** primeira instalação systemd + grupo docker (autorização) + `tailscale serve` (conferir `Host` preservado
  para o WS e conflito na 443); reboot; roteiros reais de Players e modpack. Upgrades seguintes: `sudo make deploy` (D70).
- **2026-11-20/21:** renomeação para Craftkeeper (D69) e script de deploy (D70). `sudo make check` passou com a
  migração; o container legado foi migrado e funcionou (usuário); compatibilidade removida depois. **Ainda não rodado:**
  `sudo make check` após a remoção; E2E do deploy (precisa da primeira instalação).

## Aba Resources — RAM/CPU na página do servidor (2026-11-18)

Plano: `~/.claude/plans/preciso-que-faca-o-fuzzy-haven.md`.
- **Implementado (só frontend):** aba "Resources" em `#/servers/{id}`
  (`web/assets/js/resources-manager.js`), usando o `PATCH /servers/{id}` existente (sem mudança
  de API). Editável só com status `stopped` (política da Fase 3); nos outros estados, campos
  desabilitados + nota. Salvar atualiza o cabeçalho. `limitFields` movido para
  `server-actions.js` (compartilhado com os diálogos de criar/editar da lista). Sem CSS novo.
- **Testado:** `sudo make check` passou (2026-11-18; binário 13,7 MB); sintaxe JS com `node --check`.
- **Verificado de verdade (usuário, 2026-11-18):** teste manual da aba no navegador.

## Ícone do servidor — `server-icon.png` (2026-11-18)

Plano: `~/.claude/plans/veja-com-da-suporte-glittery-milner.md`.
- **D52 · Ícone só no frontend, sobre a API de arquivos** (mesmo raciocínio da D49) — o jogo lê
  `server-icon.png` (PNG 64×64) da raiz ao iniciar; o itzg só o escreve com a env `ICON`, que
  não usamos, então o arquivo do painel é mantido. O browser recorta para quadrado e escala para
  64×64 (sem suavização se a origem tiver ≤ 64 px, para pixel art), gera PNG no canvas e envia
  por `POST /files/upload?path=&overwrite=true`; remover = `DELETE /files`. Preview via `data:`
  URL (CSP `img-src 'self' data:`, sem mudança). Origem ≤ 10 MiB. Sem mudança de API/backend.
- **Implementado:** `web/assets/js/icon-manager.js`, seção no topo da aba "Properties"
  (`pages/server.js`), com "Save and restart" quando rodando. `make css` regenerado.
- **Testado:** `sudo make check` passou; verificado pelo usuário no navegador e no jogo (2026-11-18).
- **Ícone no header e na lista** (pedido do usuário, 2026-11-18): `serverIcon(id, size)` em
  `icon-manager.js` — `<img>` direto em `/files/download` (octet-stream; o `<img>` decodifica pelo
  conteúdo; `nosniff` só afeta script/style), sem ícone = tile vazio. Header 48 px, lista 32 px
  (uma carga por linha: linhas são atualizadas no lugar). Salvar/remover recarrega o do header.
  Testado: `sudo make check` passou; conferido pelo usuário no navegador (2026-11-18).
- **Ícone do pack automático** (pedido do usuário, 2026-11-21): depois de extrair o ZIP no
  `modpack-manager.js`, `adoptPackIcon` (`icon-manager.js`) procura `icon.png` nos diretórios dos
  scripts e depois na raiz; se não há `server-icon.png` ao lado, recorta/escala para 64×64 e envia
  como `server-icon.png` **no mesmo diretório** (o jogo lê do diretório de trabalho do launcher,
  que o wrapper faz `cd`). Nunca sobrescreve. Só frontend. Limitação: o header/aba Properties leem
  só a raiz, então pack com pasta de topo não mostra o ícone no painel (o jogo mostra).
  Testado: `node --check`; falta teste no navegador.

## Players online — Server List Ping (2026-11-18)

Plano: `~/.claude/plans/veja-um-jeito-se-glimmering-tarjan.md`.
- **D53 · Quem está online via Server List Ping** — o painel faz o ping de status do cliente
  (handshake + status request, TCP) em `127.0.0.1:<porta do servidor>` (porta publicada no host,
  `gameBindIP`). Rejeitados: `list` pelo console (cada `SendCommand` grava audit e polui o
  console a cada poll), parse de "joined/left" (feed só existe com inscrito) e RCON/Query
  (desligados, D21). Limite: vanilla manda ≤ 12 nomes e nenhum com `hide-online-players=true`;
  `online`/`max` sempre certos. Sem cache (poll de 10 s por aba aberta é barato).
- **Implementado:** `infrastructure/minecraft` (stdlib, resposta ≤ 256 KiB, timeout 3 s, só
  loopback, descarta entradas com UUID zero/nome inválido), `domain.OnlinePlayers`,
  `domain.ErrGameUnreachable` (→ `503 game_unreachable`), `players.Service.Online` (port
  `Pinger`), `GET /servers/{id}/players/online` (`docs/API.md`). UI: seção "Online now" no topo
  da aba Players (avatares, `n / max`, "+N não listados"), badge "Online" na whitelist; poll a
  cada 10 s só com servidor rodando, aba visível e documento visível. `make css` regenerado.
- **Testado:** `minecraft/ping_test.go` (servidor SLP falso), contrato HTTP `TestOnlinePlayers`.
  `sudo make check` passou (2026-11-18). Verificado pelo usuário com servidor real: a listagem funcionou.

## Fase 9 — Console e eventos (2026-11-18)

Plano: `~/.claude/plans/planeje-a-fase-9-noble-kernighan.md`. Decisões D57–D59. Sem retenção do
audit log por ora (escolha do usuário).
- **Implementado:** migration `0006_audit_server`; `domain` (`AuditEvent.ServerID/Username/
  ServerName`, `AuditActions`, `AuditFilter`); `sqlite.AuditLog.List`; `application/activity`;
  `ServerID` em todo audit de servers/files/backups/players/console; `console.Send`; rota
  `GET /api/v1/audit` + campos novos em `recent_events`; frontend `activity.js` (tabela, filtros,
  "Load more"), página `#/activity` (menu "Activity"), aba "Activity" no servidor, Overview com
  rótulos e "View all"; console com histórico persistente, busca e seletor de `logs/`
  (`log-viewer.js`); `docs/API.md`, `docs/WEBSOCKET.md`.
- **Testes escritos:** `sqlite/audit_test.go` (filtros, paginação, joins, migration sobre banco
  antigo), `activity/service_test.go`, `http/audit_test.go` (contrato), console (`Send` sem
  audit, target = comando), asserções de `ServerID` em files/backups/crashguard. JS: `node --check`.
- `make css` regenerado.
- **Testado:** `sudo make check` passou (2026-11-18; gofmt, vet, testes, build; binário 13,8 MB).
- **Verificado de verdade (usuário, 2026-11-18):** roteiro manual do plano feito no navegador.

## Fase 11 — Versão, mundos e operadores (2026-11-18)

Plano: `~/.claude/plans/planeje-a-fase-10-joyful-dolphin.md`. Decisões D60–D63. Fase 10 ficou
opcional (pulada a pedido do usuário).
- **Implementado:** `domain/world.go` (`Operator`, `LevelName`, `OpLevel`, `ValidateSeed`,
  `WorldStagingPrefix`), erros `invalid_world`/`modpack_server`/`backup_failed`, audit
  `server.version_change`, `world.reset`, `world.import`, `player.op`, `player.deop`;
  `docker.RenameReplaced`; `sqlite.Servers.Update` com versão; `ServerDirs.Path`;
  `servers/version.go` (`ChangeVersion`, `ResolveVersion`) + sweep no `Recover`;
  `backups.Protect`; `application/maintenance`; `players.AddOp/RemoveOp`; rotas
  `POST /servers/{id}/version`, `/world/reset`, `/world/import`, `POST|DELETE /players/ops`;
  `main.go`. Frontend: `version-fields.js` (extraído do "New server"), aba **World**
  (`world-manager.js`: trocar versão com avisos de downgrade/loader, download, reset com seed e
  confirmação pelo nome, import com upload), seção "Operators" na aba Players, rótulos no
  Activity; `docs/API.md` (seção World, ops, erros).
- **Testes escritos:** `servers/version_test.go`, `backups/protect_test.go`,
  `maintenance/service_test.go`, `domain/world_test.go`, `docker` (`TestRenameReplaced`),
  `sqlite` (Update com versão), `http/maintenance_test.go` (contratos). JS: `node --check`.
- **Testado:** `sudo make check` passou (2026-11-18). O 1º run pegou só um bug de teste (`AuditFilter` sem `Limit` devolve vazio).
- **Verificado de verdade (usuário, 2026-11-18):** funcionou no painel (aba World e Operators).

## Fase 12 — Mods via Modrinth (2026-11-19)

Plano: `~/.claude/plans/mellow-humming-hennessy.md`. Decisões D64–D67.
- **Implementado:** `domain/mods.go` (tipos, `ValidateModrinthID`, `ValidateModFile`,
  `SafeModFileName`, `ModpackConfigFile`, limites), erros `mods_unsupported`/`mod_incompatible`/
  `modrinth_unavailable`, audit `mod.install|update|remove|enable|disable`;
  `infrastructure/modrinth` (busca, projetos, versões, by-hash, download verificado, ícone);
  `ServerFiles.FreeBytes`; `application/mods` (List, Search, Versions, Install, Update,
  SetEnabled, Remove, Icon); rotas em `presentation/http/mods.go` (`/servers/{id}/mods`,
  `/mods/search`, `/mods/projects/{project}/versions`, `/mods/update`, `/mods/files/{file}`,
  `/mods/icons/{project}`); `main.go`. Frontend: `mod-manager.js` (instalados com ícone, versão,
  Update/Update all com backup, Enable/Disable, Remove; busca paginada; diálogo de instalação
  com seletor de versão e aviso de deps; aviso "Restart" com servidor rodando), aba **Mods** em
  `pages/server.js` (escondida em vanilla; modpack explicado dentro), rótulos no Activity.
  Docs: `API.md` (seção Mods + erros), `SECURITY.md`, `ARCHITECTURE.md`.
- **Testes escritos:** `domain/mods_test.go`, `modrinth/client_test.go` (hash/tamanho/host/
  redirect, validação de versões, 404/500, SVG), `mods/service_test.go` (deps + pulo de
  instalados, release preferida, incompatível nos dois sentidos, limite de 32, download ruim não
  deixa arquivo, update com backup/desativado/deps, escopo), contrato `http/mods_test.go`
  (Modrinth falso em httptest). JS: `node --check`.
- `make css` regenerado.
- **Testado:** `sudo make check` passou (2026-11-19, inclusive após backup opcional no update; binário 14,1 MB). Diálogo de instalação conferido no navegador pelo usuário.
- **Pendente:** roteiro manual com servidor Fabric real (ver plano).

