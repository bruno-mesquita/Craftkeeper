<div align="center">

<img src="web/assets/icons/logo.svg" alt="Logo do Craftkeeper" width="96" height="96">

# Craftkeeper

**Painel leve e self-hosted para servidores de Minecraft rodando em Docker.**
Um único binário Go estático. Sem Node, sem CDN, funciona offline.

[English](README.md) · [Português](README.pt-BR.md)

![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Engine%20API-2496ED?logo=docker&logoColor=white)
![SQLite](https://img.shields.io/badge/SQLite-pure%20Go-003B57?logo=sqlite&logoColor=white)
![Tailwind CSS](https://img.shields.io/badge/Tailwind%20CSS-v4-06B6D4?logo=tailwindcss&logoColor=white)
![Plataforma](https://img.shields.io/badge/plataforma-linux%2Famd64-informational?logo=linux&logoColor=white)

</div>

O Craftkeeper gerencia game servers como containers Docker no próprio host onde roda. O
backend é Go, o frontend é HTML puro, módulos ES e Web Components com Tailwind CSS v4, e tudo
fica embutido num único binário de ~14 MB. É uma alternativa ao
[Crafty Controller](https://craftycontrol.com/) e ao [Pterodactyl](https://pterodactyl.io/)
para quem quer algo pequeno o bastante para ler e auditar.

> **Nota sobre o nome:** o produto é Craftkeeper, mas o binário Go, a unit systemd, os
> caminhos e as labels dos containers ainda usam o nome original `mesquita-panel` /
> `mesquita.managed`.

## Screenshots

<!--
Coloque as imagens em docs/images/ e descomente:

![Overview](docs/images/overview.png)
![Console](docs/images/console.png)
![Mods](docs/images/mods.png)
![Arquivos](docs/images/files.png)
-->

_Em breve._

## Funcionalidades

**Servidores**
- Criação em segundo plano: Vanilla, Fabric, Forge e NeoForge, com versões lidas das fontes
  oficiais (Mojang, Fabric, Forge, NeoForge) e fixadas na criação.
- A imagem Java certa para cada versão do Minecraft, fixada por digest.
- Troca de versão ou loader (com backup de segurança), limites de RAM e CPU.
- Detecção de crash loop: servidor preso reiniciando é parado e marcado como `crashed`, com o
  último crash report a um clique.

**Console e atividade**
- Console ao vivo por WebSocket com histórico de comandos persistente, busca e `logs/*.log.gz`
  antigos.
- Log de auditoria de toda ação administrativa, global e por servidor.

**Mods e modpacks**
- Busca e instalação de mods do [Modrinth](https://modrinth.com/) filtrados por loader e
  versão do Minecraft, com tratamento de dependências, verificação SHA-512, ativar/desativar e
  atualização com backup opcional.
- Importação de ZIP de server pack ao criar o servidor.

**Jogadores e mundos**
- Whitelist, bans, bans de IP e operadores; veja quem está online agora.
- Resetar o mundo (com seed), importar um ou baixar.

**Arquivos e backups**
- Gerenciador de arquivos com upload em streaming (até 10 GiB), editor, zip e unzip, confinado
  à pasta do servidor com `os.Root` (sem traversal, sem fuga por symlink).
- Backups online (`save-off` / `save-all flush`), restore, backups agendados com retenção.

**Segurança em primeiro lugar**
- Senhas Argon2id, sessões no servidor, CSRF em toda requisição mutante, rate limit de login,
  token de setup de uso único, trilha de auditoria.
- Nunca faz bind em `0.0.0.0` por padrão; só age em containers que ele mesmo criou
  (`mesquita.managed=true` + `server_id` conhecido); containers de jogo rodam sem root e sem
  capabilities. Veja [Security](docs/SECURITY.md).

## Arquitetura em resumo

Clean Architecture organizada por camada; as dependências apontam para dentro.

```
cmd/panel/main.go                 composition root
internal/domain/                  entidades, regras, erros (sem imports de infra)
internal/application/<feature>/   casos de uso + as portas que consomem
internal/infrastructure/<tech>/   docker, sqlite, filesystem, modrinth, ...
internal/presentation/http/       handlers REST, rotas, middlewares
internal/presentation/websocket/  transporte de console e status
migrations/                       SQL versionado, embutido
web/                              frontend embutido
```

O Docker é controlado pela Engine API via unix socket com `net/http` puro (sem SDK). Mais em
[Architecture](docs/ARCHITECTURE.md).

## Início rápido (desenvolvimento)

Go não é necessário no host; os builds rodam num container `golang` fixado.

```bash
sudo make check     # gofmt + vet + testes + build (saída em .cache/last-run.log)
make run            # http://127.0.0.1:8090, dados em ./.data
make css            # regenera web/assets/css/app.css após mudar classes
```

No primeiro start o log imprime um token de setup de uso único; use-o para criar o
administrador.

## Deploy

O Craftkeeper roda como serviço systemd atrás de loopback, opcionalmente com HTTPS via
`tailscale serve`. Passo a passo: [Installation](docs/INSTALL.md). Todas as variáveis de
ambiente: [Configuration](docs/CONFIGURATION.md).

## Status

| Fase | Escopo | Status |
|---|---|---|
| 1–7 | Fundação, monitoramento, gestão de servidores, console, arquivos, backups, instaladores | ✅ implementado e testado |
| 8 | Guarda contra crash loop | ✅ verificado com servidor real; deploy systemd pendente |
| 9 | Histórico/busca/logs antigos do console, log de atividade | ✅ concluída |
| 11 | Troca de versão/loader, mundos, operadores | ✅ concluída |
| 12 | Mods via Modrinth | 🚧 implementada, verificação com servidor real pendente |
| 13–14 | Histórico de métricas, página global de backups, clonar/templates | 📋 planejadas |

Registro detalhado e decisões: [`MEMORY.md`](MEMORY.md) (em português).

## Documentação

Os documentos técnicos estão em inglês.

| | |
|---|---|
| [Specification](docs/SPEC.md) | Requisitos, fonte de verdade |
| [Architecture](docs/ARCHITECTURE.md) | Camadas, componentes, política Docker |
| [Security](docs/SECURITY.md) | Modelo de ameaças e controles |
| [Development](docs/DEVELOPMENT.md) | Build, testes, convenções |
| [Installation](docs/INSTALL.md) | Ubuntu + systemd |
| [Configuration](docs/CONFIGURATION.md) | Variáveis de ambiente e limites fixos |
| [REST API](docs/API.md) | Referência de `/api/v1` |
| [WebSocket protocol](docs/WEBSOCKET.md) | Formato das mensagens versionadas |
