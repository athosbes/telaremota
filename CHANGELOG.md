# Histórico de versões

Todas as mudanças relevantes do TelaRemota. O formato segue o [Keep a Changelog](https://keepachangelog.com/pt-BR/1.1.0/) e as versões seguem o [Versionamento Semântico](https://semver.org/lang/pt-BR/).

## [Não publicado]

### Adicionado

- Ao instalar o serviço, o TelaRemota cria o atalho no menu Iniciar e aparece em *Configurações → Aplicativos instalados*, de onde é desinstalado.
- Comando `telaremota desinstalar [-apagar-dados] [-silencioso]`: remove serviço, firewall, atalho, entrada e a pasta do programa; pergunta se apaga também a identidade e os pareamentos.
- Ícone embutido no `telaremota.exe` (Explorer, atalhos, barra de tarefas fixada) e detalhes do arquivo (descrição, produto, copyright) em Propriedades e no Gerenciador de Tarefas.

### Alterado

- Ícone redesenhado para ficar legível em 16 px: tela maior e seta mais grossa. As janelas usam o tamanho certo de cada lugar em vez de reduzir o PNG.

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
