# Changelog

Formato baseado em [Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/); versões seguem [SemVer](https://semver.org/lang/pt-BR/).

## [1.0.0]

Primeira versão estável.

### Funcionalidades
- Caixas de e-mail descartáveis que expiram em 5 minutos (TTL fixo, não renova com novas mensagens), sem cadastro.
- **Caixas por nome na URL:** `/<nome>` (3 a 32 caracteres) abre ou cria a caixa; nomes curtos, longos, inválidos ou reservados voltam para a página inicial com a explicação.
- Página inicial de boas-vindas; atalhos para as caixas abertas no navegador; "+ Nova caixa" abre outra aba.
- **API REST** com long-poll: `PUT /api/inbox/{nome}` (idempotente), `POST /api/inbox`, listar/ler mensagens, baixar anexos e o `.eml` original, apagar a caixa.
- `AUTO_CREATE_ON_MAIL` (desligado por padrão): um e-mail para um nome válido ainda inexistente cria a caixa.
- **Rotação por teto de memória** (`MAX_STORE_MB`, padrão 1024): apaga as mensagens mais antigas em vez de recusar e-mails novos.
- Binário Go único, imagem `scratch` de ~11 MB, tudo em memória (sem banco). Docker Compose e manifests de Kubernetes (1 réplica).

### Segurança
- SMTP somente recebe: sem relay, sem bounces; recipientes inexistentes recebem `550`.
- Limites de conexões SMTP (total e por IP), de tamanho, de mensagens em processamento e de partes MIME; rate limit por IP na API.
- HTML de e-mail em `iframe` com `sandbox` e CSP restritiva; anexos sempre como download opaco com nome sanitizado.
- Container não-root, sistema de arquivos somente leitura, sem capabilities; configuração inválida impede a inicialização.
- Compilado com Go 1.26; `govulncheck` sem vulnerabilidades.

### Limitações conhecidas
- Sem autenticação: quem souber o nome da caixa a lê. Não use para dados sensíveis (veja `SECURITY.md`).
- Estado em memória: reiniciar apaga as caixas; apenas 1 réplica.
