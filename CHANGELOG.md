# Histórico de versões

Todas as mudanças relevantes do TelaRemota. O formato segue o [Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/) e as versões seguem o [Versionamento Semântico](https://semver.org/lang/pt-BR/).

## [Não publicado]

## [0.5.0] - 2026-11-17

Primeira versão pública.

### Adicionado

- Acesso remoto direto para Windows 10/11, sem servidores de terceiros: um computador é o **ponto fixo** e os demais se pareiam a ele por **código de convite**.
- Cifra de **ponta a ponta** com a chave de cada computador fixada no pareamento, e **senha de acesso** por computador (acesso autônomo, inclusive com a tela bloqueada).
- **Rede Tailscale embutida**: casa ↔ empresa atrás de CGNAT sem instalar o Tailscale nem abrir portas.
- **Modo técnico**: um computador pode só controlar os outros, sem aceitar acesso.
- **Várias telas**: escolha uma ou todas pelo menu da janela ou com Ctrl+Alt+←/→.
- Captura pela GPU (DXGI) até 60 quadros por segundo, com GDI de reserva.
- Ponto fixo opcional em servidor Linux (amd64 e arm64), com administração remota.
- Abertura automática da porta no roteador (UPnP) e suporte a IPv6.

[Não publicado]: https://github.com/athosbes/telaremota/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/athosbes/telaremota/releases/tag/v0.5.0
