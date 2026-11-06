# tempmail

E-mail descartável **leve e sem cadastro**: gere uma caixa, receba mensagens (com anexos) por uma
interface web ou pela API, e ela some sozinha em 5 minutos. Binário Go único, imagem de ~10 MB,
tudo em memória, sem banco de dados.

- **Web UI** e **API REST** (com long-poll, ideal para automação/testes que esperam um código).
- **Rotação por teto de memória** (padrão 1 GB): em carga, apaga as mensagens mais antigas em vez de recusar.
- **Seguro por padrão**: ver [SECURITY.md](SECURITY.md). **Não** é para dados sensíveis (sem autenticação).
- Roda em Docker Compose ou Kubernetes, atrás de qualquer proxy/ingress.

## Início rápido (local)

```sh
cp .env.example .env          # defina DOMAIN (para testes locais: DOMAIN=localhost)
docker compose up -d --build
# UI: http://127.0.0.1:8080   SMTP: porta 25 do host
```

## Deploy

Você precisa de um domínio e de um servidor com a **porta 25 de entrada liberada**
(muitos provedores de nuvem bloqueiam por padrão).

1. **DNS** (substitua `example.com` pelo seu domínio):

   | Tipo | Nome | Dados | Prioridade |
   |------|------|-------|------------|
   | A    | `mail` | IP público do servidor | |
   | MX   | `mail` | `mail.example.com` | 10 |

   Os endereços ficam `xxxxxxxxxxxx@mail.example.com`; o e-mail do domínio raiz não é afetado.

2. **Subir**: `DOMAIN=mail.example.com` no `.env` e `docker compose up -d --build`.
   O HTTP escuta só em `127.0.0.1:8080`; coloque um proxy com TLS na frente:
   - **Caddy no host**: `mail.example.com { reverse_proxy 127.0.0.1:8080 }`
   - **Caddy em Docker**: `docker compose -f docker-compose.yml -f docker-compose.caddy.yml up -d --build`
     (ajuste o nome da rede no override) e `reverse_proxy tempmail:8080`.
   - **nginx / Traefik / outro**: `proxy_pass http://127.0.0.1:8080` (long-poll: timeout >= 60 s).
   - `TRUST_PROXY=true` (padrão do compose) vale **somente** com proxy que sobrescreve `X-Forwarded-For`.
     Sem proxy, use `TRUST_PROXY=false`.

3. **Teste**: abra o site, copie o endereço e envie um e-mail de outro provedor.

### Kubernetes

Manifests em `deploy/k8s` (Deployment, 2 Services, Ingress genérico, kustomization).

```sh
docker build -t registry.example.com/infra/tempmail:1.0.0 . && docker push registry.example.com/infra/tempmail:1.0.0
# edite imagem (kustomization.yaml, bloco images), DOMAIN (deployment.yaml) e host (ingress.yaml)
kubectl create ns tempmail && kubectl apply -k deploy/k8s -n tempmail
kubectl get svc tempmail-smtp -n tempmail   # EXTERNAL-IP -> registros A e MX
```

- **Uma réplica apenas.** O estado vive na memória do processo; com 2+ pods o e-mail pode cair em
  outro pod. Por isso `replicas: 1` e `strategy: Recreate`. Escalar exigiria um store compartilhado.
- **SMTP não passa por Ingress** (é L4): o Service `tempmail-smtp` é `LoadBalancer`
  (MetalLB, NLB...) com `externalTrafficPolicy: Local` para preservar o IP de origem, necessário
  para os limites por IP. Sem LB, use NodePort/hostPort.
- HTTP: qualquer Ingress ou Gateway API (`HTTPRoute` -> Service `tempmail-http:80`).
  Com ingress-nginx, `proxy-read-timeout` >= 60 s.
- Pod não-root, FS somente leitura, sem capabilities, seccomp RuntimeDefault (Pod Security `restricted`).
  Pode ser necessária uma NetworkPolicy: liberar entrada em 8080 (ingress) e 2525 (qualquer origem).

## Caixas por nome

O endereço funciona como URL, sem informar o domínio: abrir `https://mail.example.com/automacao-123`
abre a caixa `automacao-123@mail.example.com`, **criando-a se não existir** (3 a 32 caracteres de
`a-z 0-9 . _ -`; `api` e `healthz` são reservados). Com `AUTO_CREATE_ON_MAIL=true`, um e-mail
enviado para um nome válido que ainda não existe também cria a caixa, então a automação pode
enviar antes de alguém abrir a página. O TTL de 5 minutos conta da criação.

Na UI, **+ Nova caixa** abre outra aba com um nome aleatório. Caixas abertas neste navegador
aparecem como atalhos no topo.

## API

| Método | Rota | Descrição |
|---|---|---|
| PUT | `/api/inbox/{nome}` | Abre ou cria a caixa (idempotente): 200 se existia, 201 se criou |
| POST | `/api/inbox` | Cria caixa. Corpo opcional `{"name":"meunome"}`; sem corpo, nome aleatório |
| GET | `/api/inbox/{addr}/messages?wait=25&after=0` | Lista mensagens. `wait` = long-poll (máx 30 s), `after` = último id visto |
| GET | `/api/inbox/{addr}/messages/{id}` | Texto, HTML e lista de anexos |
| GET | `/api/inbox/{addr}/messages/{id}/attachments/{n}` | Baixa o anexo |
| GET | `/api/inbox/{addr}/messages/{id}/raw` | E-mail original `.eml` |
| DELETE | `/api/inbox/{addr}` | Apaga a caixa antes do prazo |
| GET | `/api/stats` | Só com `EXPOSE_STATS=true` |

```sh
A=$(curl -s -X POST https://mail.example.com/api/inbox | jq -r .address)
curl -s "https://mail.example.com/api/inbox/$A/messages?wait=30" | jq
```

## Configuração (variáveis de ambiente)

Valores inválidos impedem a inicialização (não há fallback silencioso).

| Var | Padrão | |
|---|---|---|
| `DOMAIN` | **obrigatório** | domínio dos endereços |
| `TTL_SECONDS` | `300` | vida da caixa (não renova com novas mensagens) |
| `MAX_STORE_MB` | `1024` | teto de memória das mensagens (ver "Rotação") |
| `MAX_INBOXES` | `20000` | caixas simultâneas (acima, criar retorna 503) |
| `MAX_MSGS_PER_INBOX` | `30` | acima disso a mais antiga da caixa rotaciona |
| `MAX_MSG_BYTES` | `5242880` | tamanho máx. por e-mail (com anexos) |
| `CREATE_PER_MIN` / `API_PER_MIN` | `10` / `240` | limites por IP |
| `SMTP_MAX_CONNS` / `SMTP_MAX_CONNS_PER_IP` / `SMTP_MAX_DATA` | `256` / `10` / `32` | conexões SMTP e mensagens em processamento simultâneo |
| `AUTO_CREATE_ON_MAIL` | `false` | e-mail para um nome válido ainda inexistente cria a caixa (ver "Caixas por nome") |
| `AUTO_CREATE_PER_MIN` | `60` | caixas criadas por e-mail, por IP do remetente/minuto |
| `TRUST_PROXY` | `false` | `true` só atrás de proxy que sobrescreve `X-Forwarded-For` |
| `EXPOSE_STATS` | `false` | habilita `GET /api/stats` |
| `HTTP_ADDR` / `SMTP_ADDR` | `:8080` / `:2525` | endereços de escuta |

## Rotação (limite de memória)

Ao atingir `MAX_STORE_MB`, o serviço **não recusa e-mails**: apaga as mensagens mais antigas
(FIFO global) até caber a nova. Por caixa vale o mesmo (`MAX_MSGS_PER_INBOX`), porque a mensagem
esperada (código, link) é sempre a mais recente.

Dimensionamento: `limite de memória do container >= MAX_STORE_MB x 1,5`. Medido com teto de 1 GB
num container de 1,5 GB: ~5,5 GB de e-mails trafegados, 1626 rotacionados, sem falhas nem OOM
(pico 1,25 GiB). O app ajusta `GOMEMLIMIT` sozinho. Se mudar `MAX_STORE_MB`, ajuste `MEM_LIMIT`
(compose) / `limits.memory` (k8s).

## Desenvolvimento

Sem Go instalado:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.26-alpine sh -c \
  'apk add -q build-base && go vet ./... && go test -race ./...'
```

Estrutura: `cmd/tempmail` (composição) · `internal/{inbox,smtpd,httpapi,mailparse,ratelimit,config}`.
Veja [CONTRIBUTING.md](CONTRIBUTING.md). Licença: [MIT](LICENSE).
