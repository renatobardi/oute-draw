# Deploy no oute-server

O oute-draw entra no host do mesmo jeito que o Backstage (oute-infra-ops ADR-0010, ADR-0011,
ADR-0018): um LXC com Docker Compose, o oauth2-proxy na frente com login Google, estado no
`oute-supabase-db` e deploy por pull da imagem do GHCR. Este repositório entrega a imagem e
o `deploy/compose.yaml`; **o que existe** (LXC, vhost, certificado, tenant) é declarado no
`oute-catalog` e **aplicado** pelo `oute-infra-ops`.

Nada aqui foi aplicado ainda. A ordem abaixo é a que evita subir algo quebrado.

## 1. Imagem

O merge na `main` publica `ghcr.io/renatobardi/oute-draw:latest` (arm64) e roda um smoke
dela. Se o pacote do GHCR for privado, o LXC precisa de `docker login ghcr.io` com um token
de leitura de pacotes, como o `backstage-prd`.

## 2. Catálogo (`oute-catalog`)

Pelo template "register app" do Backstage (ADR-0013) ou por PR, uma entidade por arquivo:

| Entidade | `spec.type` | Observações |
|---|---|---|
| `draw-prd` | `lxc` | 512 MiB de limite bastam (o processo usa dezenas de MiB); `dependsOn` host e rede; `partOf` o System do app |
| `draw.oute.pro` | `vhost` | `upstream: resource:draw-prd`, porta do oauth2-proxy; `client_max_body_size` 50m (o padrão) |
| `draw.oute.pro` | `certificate` | — |
| `draw-prd` | `database` | tenant Postgres: schema `draw_prd`, papel `app_draw_prd`; `dependsOn` o LXC do `oute-supabase-db` |

O `catalog-info.yaml` deste repositório já declara o Component; falta o System se você
quiser agrupar.

## 3. Tenant no `oute-supabase-db`

Formato padrão "um schema por app" (ADR-0003): papel `app_draw_prd` dono do schema
`draw_prd`. O app **não cria schema nem extensão**: só tabelas dentro do schema que recebe,
e aplica as próprias migrações ao subir. Defina `DRAW_DB_SCHEMA=draw_prd` (ou deixe o
`search_path` do papel apontando para ele).

O backup vem junto no dump do supabase (ADR-0015). Os desenhos, as imagens e o histórico
estão todos nesse schema: é o único estado do app.

## 4. Login Google

Um cliente OAuth próprio no Google Cloud, com redirect
`https://draw.oute.pro/oauth2/callback`. Client id, client secret e cookie secret vão para o
Vaultwarden e daí para o `.env`.

## 5. Comportamento do vhost (`oute-infra-ops`)

Em `ansible/roles/vhosts/defaults/main.yml`, uma entrada em `vhosts_behaviour`, igual à do
Backstage no essencial:

```yaml
  draw.oute.pro:
    security_headers: true
    websocket: true
    http: ["limit_req_zone $binary_remote_addr zone=draw_ip:10m rate=20r/s;"]
    server: ["limit_req zone=draw_ip burst=200 nodelay", "limit_req_status 429"]
    # oauth2-proxy sets the identity headers; whatever a client sends is cleared first.
    location:
      - proxy_set_header X-Forwarded-Email ""
      - proxy_set_header X-Forwarded-User ""
      - proxy_set_header X-Forwarded-Preferred-Username ""
      - proxy_set_header X-Forwarded-Groups ""
      - proxy_set_header X-Forwarded-Access-Token ""
      - proxy_set_header X-Oauth2-Proxy-Id-Token ""
      - proxy_read_timeout 3600
```

Três pontos que não são opcionais:

- **`websocket: true` e o `proxy_read_timeout` longo**: a colaboração ao vivo é um WebSocket
  que fica aberto.
- **Limpar os headers de identidade**: o app confia no `X-Forwarded-Email`. Sem isso, e sem
  o oauth2-proxy na frente, qualquer um se passaria por você.
- **`security_headers: true` manda `X-Frame-Options: DENY`**, o que desliga o "código de
  incorporação" dos links (o iframe é recusado pelo navegador). Se quiser incorporar
  desenhos em outros sites, este vhost precisa ficar sem esse header; o app já restringe o
  enquadramento das páginas do workspace por conta própria (`frame-ancestors 'self'`) e
  libera só as páginas `/s/…`.

O firewall do LXC deve aceitar a porta do oauth2-proxy só do endereço da bridge do host,
como no `backstage-prd` (`table inet oute_backstage`).

## 6. No LXC

```sh
git clone https://github.com/renatobardi/oute-draw /opt/app
cd /opt/app
cp deploy/env.example .env      # preencha com os valores do Vaultwarden
printf '%s\n' voce@exemplo.com > emails.txt   # quem pode entrar
docker compose -f deploy/compose.yaml --env-file .env up -d
docker compose -f deploy/compose.yaml --env-file .env ps   # draw: healthy
```

O compose recusa subir sem `DRAW_DATABASE_URL`, `DRAW_SECRET`, `DRAW_PUBLIC_URL` e
`DRAW_PUBLICAR`. O container do app não publica porta: só o oauth2-proxy o alcança.

Quem entra é a lista `emails.txt`. O que cada pessoa pode fazer é o papel dentro do app:
`DRAW_ADMIN_EMAILS` define os admins, os demais entram com `DRAW_DEFAULT_ROLE`, e um admin
muda papéis em **Membros**.

## 7. Deploy contínuo

O timer `oute-backstage-deploy` (ADR-0018) é específico do Backstage: imagem, container e
arquivo de ambiente estão fixos no script. Para o draw, o caminho é generalizar esse role
para receber o app como parâmetro (LXC, imagem, container, diretório) e instanciá-lo duas
vezes; até lá, o deploy é `docker compose pull && up -d` à mão no LXC. O healthcheck do
container (`/oute-draw -healthcheck`) já serve de portão para o rollback do script.

## 8. Monitoramento

Dois monitores no Uptime Kuma, sem rota nova sem login:

- `https://draw.oute.pro/ping` — o `/ping` do próprio oauth2-proxy (nginx, certificado, proxy);
- o healthcheck do container, ou `GET /healthz` de dentro do LXC — o app e o banco.

## 9. Conferência depois de subir

1. `https://draw.oute.pro` pede login Google e abre o dashboard.
2. Criar um desenho, desenhar, voltar: a miniatura aparece.
3. Abrir o mesmo desenho em duas abas: o cursor de uma aparece na outra (WebSocket passando
   pelo nginx).
4. Criar um link de leitura e abri-lo numa janela anônima: abre sem login.
5. Na janela anônima, `https://draw.oute.pro/api/scenes` deve responder 401 ou ir para o login.

## Migrando do Excalidraw+

No workspace do Excalidraw+, exporte os desenhos como arquivos `.excalidraw`. No dashboard do
oute-draw, **Importar** aceita vários arquivos de uma vez e leva as imagens junto; importe
dentro de uma coleção para já deixá-los organizados. Comentários, histórico e links do
produto hospedado não vêm no arquivo e não são migrados.
