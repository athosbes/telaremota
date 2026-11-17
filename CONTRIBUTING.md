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

## Publicar uma versão (mantenedores)

1. Atualize o [CHANGELOG.md](CHANGELOG.md): mova os itens de **[Não publicado]** para uma seção `## [X.Y.Z] - AAAA-MM-DD` (versionamento semântico: correção = Z, função nova = Y, quebra de compatibilidade = X).
2. Opcional: teste os pacotes localmente com `bash scripts/release.sh vX.Y.Z` (saída em `dist/release/`).
3. Crie e envie a tag:

   ```bash
   git tag -a vX.Y.Z -m "TelaRemota X.Y.Z"
   git push origin vX.Y.Z
   ```

O workflow [release.yml](.github/workflows/release.yml) roda os testes, compila Windows (amd64) e o ponto fixo Linux (amd64/arm64) com a versão embutida, inclui `LICENSE`, `README.md` e as licenças das bibliotecas (`THIRD_PARTY_LICENSES.txt`) em cada pacote, gera `SHA256SUMS.txt` e publica o Release com as notas do CHANGELOG. Tags com sufixo (ex.: `v0.6.0-beta.1`) saem como pré-lançamento.

## Segurança

**Não abra issue pública para vulnerabilidades.** Use o recurso *Report a vulnerability* (aviso de segurança privado) da página do repositório, com passos para reproduzir. Responderemos assim que possível.
