# Como contribuir

Obrigado pelo interesse! Contribuições são bem-vindas: correções, testes, documentação e novas funções.

## Ambiente

- **Go 1.27.4 ou mais novo** (veja `go.mod`).
- **Windows 10/11 64 bits** para o executável principal (`cmd/telaremota` usa APIs Win32).
- O ponto fixo para servidores (`cmd/telaremota-hub`) compila em qualquer sistema.

## Compilar e testar

```bash
go build ./...          # tudo (no Windows)
go vet ./...
go test ./...
```

Para gerar os binários de distribuição em `dist/`: `.\build.ps1` (PowerShell) ou `make` (Git Bash/Linux/macOS com `make`).

Para testar sem instalar o serviço, rode em primeiro plano, em dois terminais:

```powershell
telaremota ponto-fixo -agente     # ponto fixo + agente
telaremota conectar <nome> -diag 5s
```

`scripts\teste-tailnet.ps1` faz um teste de ponta a ponta da rede Tailscale embutida (pede uma auth key de teste).

## Enviando mudanças

1. Abra uma *issue* antes de mudanças grandes, para combinarmos a abordagem.
2. Mantenha os commits pequenos e com mensagem clara (o projeto usa português do Brasil em código, mensagens e documentação).
3. Rode `gofmt`, `go vet ./...` e `go test ./...` antes de enviar. O CI faz o mesmo.
4. Mudanças em criptografia, pareamento ou autorização (`internal/identity`, `internal/e2e`, `internal/access`, `internal/hub`) precisam de explicação do modelo de ameaça na descrição do *pull request*.

Ao contribuir, você concorda que sua contribuição seja distribuída sob a [licença do projeto](LICENSE).

## Segurança

**Não abra issue pública para vulnerabilidades.** Use o recurso *Report a vulnerability* (aviso de segurança privado) da página do repositório, com passos para reproduzir. Responderemos assim que possível.
