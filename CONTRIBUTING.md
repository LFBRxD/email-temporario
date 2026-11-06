# Contribuindo

1. Faça um fork e crie um branch.
2. Rode a verificação completa antes do PR (não precisa de Go instalado):
   ```sh
   docker run --rm -v "$PWD:/src" -w /src golang:1.26-alpine sh -c \
     'apk add -q build-base && test -z "$(gofmt -l .)" && go vet ./... && go test -race ./...'
   ```
3. Mantenha o projeto **pequeno e leve** (pouquíssimas dependências, sem frameworks).
4. Toda mudança de comportamento precisa de teste. Mudanças de segurança precisam de teste de abuso
   (limites, entradas hostis).
5. Estrutura: `internal/inbox` (estado) · `internal/smtpd` · `internal/httpapi` ·
   `internal/mailparse` · `internal/ratelimit` · `internal/config`. Os pacotes dependem de
   interfaces pequenas definidas por quem consome; `cmd/tempmail` só faz a composição.
6. **Privacidade:** não commite domínios, IPs, e-mails ou tokens reais. Use `example.com`.
   Confira `git config user.email` antes de commitar (use o e-mail `noreply` do GitHub).

Vulnerabilidades: veja [SECURITY.md](SECURITY.md).
