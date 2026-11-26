# oute-draw

Quadro branco do oute: um workspace de desenhos no estilo Excalidraw, auto-hospedado no
`oute-server`. Um binário Go serve a API, a colaboração ao vivo e o app web embutido; o
estado fica no Postgres compartilhado (`oute-supabase-db`).

O editor de canvas é o pacote open source [`@excalidraw/excalidraw`](https://github.com/excalidraw/excalidraw)
(licença MIT), usado como biblioteca. Todo o resto — o que no produto hospedado é o plano
pago — é deste repositório: workspace, armazenamento, colaboração, links, histórico,
comentários, apresentações e chamadas.

## O que tem

| Recurso | Como funciona aqui |
|---|---|
| Workspace | Dashboard com miniaturas, coleções, busca por título **e pelo texto dentro do desenho**, duplicar, lixeira |
| Salvamento | Automático e incremental; o servidor faz merge por elemento, então dois editores nunca se sobrescrevem |
| Colaboração ao vivo | WebSocket: cursores, seleção e edição em tempo real; reconecta e ressincroniza sozinho |
| Links de compartilhamento | Sem login: leitura ou edição, senha opcional, validade, revogação; link de apresentação e código de incorporação |
| Comentários | Pinos no canvas, conversas com respostas, resolver/reabrir, atualização ao vivo |
| Histórico de versões | Pontos automáticos + versões nomeadas, prévia e restauração (que também pode ser desfeita) |
| Apresentações | Cada frame é um slide: ordenar, apresentar em tela cheia com laser, exportar **PDF** e **PPTX** |
| Chamada de voz e tela | WebRTC ponto a ponto entre quem está no desenho |
| Biblioteca | Biblioteca pessoal de formas salva no servidor |
| Texto para diagrama (IA) | Opcional, com a sua chave da Claude API (`ANTHROPIC_API_KEY`); Mermaid para diagrama funciona sempre |
| Membros e papéis | Admin, editor e leitor; quem entra é decidido no login (oauth2-proxy + Google) |
| Migração | Importa arquivos `.excalidraw` (vários de uma vez), com as imagens |

### O que é diferente do produto hospedado

- **Um workspace só**, com três papéis. Não há times nem permissão por coleção.
- **Sem criptografia ponta a ponta**: o servidor é seu e guarda os desenhos em claro no seu Postgres.
- **Chamadas sem TURN por padrão**: atrás de NAT restritivo é preciso configurar um (`DRAW_ICE_SERVERS`).
- **Sem notificações** (e-mail, @menções) e sem modo "seguir o apresentador".
- A IA cobre texto para diagrama; os outros recursos de IA do produto hospedado não existem aqui.

## Arquitetura

```
navegador ── nginx (host) ── oauth2-proxy ── oute-draw (Go) ── Postgres (schema draw_prd)
                              login Google     API + WebSocket + app web embutido
```

- `cmd/oute-draw` — o binário. `internal/server` — HTTP, WebSocket e autorização.
  `internal/store` — a única parte que fala com o Postgres (migrações embutidas, aplicadas na
  subida). `internal/scene` — merge de elementos por versão. `internal/hub` — salas de
  colaboração em memória (só retransmitem; nada se perde se caírem).
- `web/` — React + Vite. O build vai para `web/dist` e é embutido no binário.
- **Identidade**: o app confia no header `X-Forwarded-Email` que o oauth2-proxy define. Por
  isso só o proxy alcança o app, e o nginx limpa esse header de qualquer cliente. As únicas
  rotas sem login são as dos links de compartilhamento, que só respondem a um token válido.

## Desenvolvimento

Precisa de Go, Node 24 e um Postgres com um schema em que o papel possa criar tabelas.

```sh
make web     # build do app web (necessário antes do primeiro `go build`)
make dev     # API em :8080, logado como admin (DRAW_AUTH_MODE=dev)
cd web && npm run dev   # UI em :5173 com proxy para a API
make check   # o que o CI roda: gofmt, vet, tipos e testes
```

Os testes do servidor rodam contra um Postgres real (`DRAW_TEST_DATABASE_URL`) e apagam as
tabelas do schema de teste (`DRAW_TEST_DB_SCHEMA`, padrão `draw_test`).

## Configuração

| Variável | Padrão | Para quê |
|---|---|---|
| `DRAW_DATABASE_URL` | — | Conexão Postgres (obrigatória) |
| `DRAW_DB_SCHEMA` | — | `search_path` das conexões; use o schema do tenant |
| `DRAW_SECRET` | — | Assina a prova de senha dos links; mínimo 32 caracteres |
| `DRAW_AUTH_MODE` | `proxy` | `proxy` confia no header do oauth2-proxy; `dev` loga todos como admin (**nunca em rede**) |
| `DRAW_ADMIN_EMAILS` | — | E-mails que são admin, separados por vírgula |
| `DRAW_DEFAULT_ROLE` | `editor` | Papel de quem entra pela primeira vez (`editor` ou `viewer`) |
| `DRAW_LISTEN` | `:8080` | Endereço de escuta |
| `ANTHROPIC_API_KEY` | — | Liga o texto para diagrama |
| `DRAW_AI_MODEL` | `claude-opus-5-5` | Modelo usado pela IA |
| `DRAW_ICE_SERVERS` | STUN público | Servidores ICE das chamadas, em JSON |

## Deploy

Imagem arm64 no GHCR a cada merge na `main`; o `oute-server` puxa. O passo a passo para
colocar no ar está em [`docs/deploy.md`](docs/deploy.md).
