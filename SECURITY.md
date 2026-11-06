# Política de segurança

## Reportar uma vulnerabilidade

**Não abra uma issue pública.** Use o recurso *Report a vulnerability* da aba **Security** do
repositório (GitHub private vulnerability reporting). Inclua passos de reprodução e a versão/commit.

## Modelo de ameaça (o que o projeto promete e o que não promete)

- Todo conteúdo recebido (cabeçalhos, HTML, anexos, nomes de arquivo) é **não confiável**.
- Não há autenticação: **quem souber o endereço lê a caixa**. Endereços gerados têm 60 bits
  aleatórios; nomes escolhidos pelo usuário (`{"name": ...}`) são adivinháveis. Nunca use para
  dados sensíveis, recuperação de contas importantes ou 2FA de alto valor.
- Qualquer pessoa pode abrir `/<nome>` e **criar** uma caixa com esse nome (limitado por IP). Quem
  chegar primeiro "ocupa" o nome até o TTL expirar. Com `AUTO_CREATE_ON_MAIL=true`, um remetente
  também pode criar caixas (até `AUTO_CREATE_PER_MIN` por IP) e ocupar vagas de `MAX_INBOXES`; por
  isso essa opção vem **desligada** por padrão. O SMTP continua recusando domínios que não são o seu.
- O serviço só **recebe** e-mails: não envia, não repassa (sem relay) e não gera bounces.
- As mensagens ficam somente na RAM e não são gravadas em disco. O serviço não registra em log
  endereços, IPs nem conteúdo.
- A existência de uma caixa pode ser descoberta pela resposta `550` do SMTP (inerente ao protocolo).

## Defesas implementadas

SMTP aceita apenas destinatários de caixas ativas; limites de conexões (total e por IP), de tamanho,
de mensagens simultâneas em memória e de partes MIME; rotação por teto de memória; rate limit por IP;
HTML de e-mail em `iframe` com `sandbox` (sem scripts) e CSP que bloqueia recursos remotos;
anexos sempre como `application/octet-stream` + `attachment`; nomes de arquivo sanitizados;
cabeçalhos `nosniff`, CSP restritiva, `X-Frame-Options`; container não-root, FS somente leitura.

## Responsabilidade do operador

- Ative `TRUST_PROXY=true` **apenas** atrás de um proxy que sobrescreve `X-Forwarded-For`.
- Termine TLS no proxy/ingress e considere HSTS lá.
- Mantenha a imagem e as dependências atualizadas (o CI roda `govulncheck`).
