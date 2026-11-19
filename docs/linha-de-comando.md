# Linha de comando e detalhes avançados

Tudo o que o painel gráfico faz também pode ser feito pelo terminal. Rode `telaremota` sem argumentos para ver a ajuda completa. Comandos de máquina (`servico`, `parear`, `convite`) exigem **terminal como administrador**.

## Instalação (empresa = ponto fixo, casa = atrás de CGNAT)

**1. Na empresa**

```powershell
telaremota servico instalar -ponto-fixo -agente -nome empresa
telaremota convite
```

O serviço abre a porta UDP 47070 no roteador via UPnP e no Firewall do Windows; seu usuário é pareado automaticamente ao ponto fixo local. O `convite` mostra o código e o endereço público.

**2. Em casa**

```powershell
telaremota parear <endereço-da-empresa> <código> -nome casa
telaremota servico instalar -agente
```

**3. Usar**

```powershell
telaremota lista
telaremota conectar casa
telaremota conectar empresa
```

Com a rede Tailscale embutida, acrescente `-tailnet -tailnet-chave tskey-auth-...` ao `servico instalar` (veja [tailscale.md](tailscale.md)).

## Administração

| Comando | Efeito |
|---|---|
| `telaremota dispositivos` | Lista os dispositivos pareados (no ponto fixo). |
| `telaremota remover <nome>` | Revoga o acesso; conexões ativas caem em até 10 s. |
| `telaremota convite -usos 1 -validade 5m` | Convite mais restrito. |
| `telaremota parear-local` | (Re)pareia seu usuário ao ponto fixo deste computador. |
| `telaremota senha [-remover]` | Define ou remove a senha de acesso deste computador. |
| `telaremota servico remover` | Remove o serviço e a regra de firewall (o programa continua instalado). |
| `telaremota desinstalar [-apagar-dados] [-silencioso]` | Remove o programa: serviço, firewall, atalho, entrada em *Aplicativos instalados* e `C:\Program Files\TelaRemota`. A identidade e os pareamentos só são apagados com `-apagar-dados` (ou confirmando na janela). É o que *Configurações → Aplicativos → TelaRemota → Desinstalar* executa. |
| `telaremota conectar <nome> -diag 5s` | Testa a conexão sem abrir janela (quadros, taxa, latência do primeiro quadro). |
| `scripts\diagnostico-rede.ps1` | Mostra se o computador tem IP público, CGNAT, IPv6, UPnP e o tipo de NAT. |

No ponto fixo em Linux, os equivalentes são `telaremota-hub convite`, `dispositivos`, `remover` e `desinstalar`. Usuários pareados com um **convite de administrador** (`telaremota-hub convite -admin`) geram convites, listam e revogam dispositivos pelo painel do Windows, sem acessar o servidor.

## Dados e logs

- `%ProgramData%\TelaRemota` — dados da máquina (só SYSTEM/Administradores). Log do serviço: `telaremota.log`.
- `%APPDATA%\TelaRemota` — dados do usuário (chaves conhecidas em `known_peers.json`, senhas lembradas cifradas pelo Windows/DPAPI).

## Requisitos de rede do ponto fixo

O ponto fixo precisa receber UDP na porta 47070: IP público sem CGNAT (UPnP ou redirecionamento manual), **ou** IPv6 com a porta liberada no firewall IPv6 do roteador, **ou** a rede Tailscale embutida. Se o IP público mudar, use um nome DNS (ex.: DNS dinâmico) no `parear`.

## Como a sessão funciona

- Captura pela GPU (DXGI Desktop Duplication, com GDI de reserva) até 60 qps → blocos 64×64 alterados → JPEG codificado/decodificado em paralelo.
- Várias telas: escolha uma ou todas pelo menu da janela ou Ctrl+Alt+←/→.
- Mouse em coordenadas absolutas; teclado por **scancode** (respeita ABNT2); área de transferência de texto nos dois sentidos; controle de fluxo por confirmação de quadros.
- O serviço roda como SYSTEM desde o boot. Para cada sessão, cria um processo auxiliar **na sessão do usuário** (`CreateProcessAsUser`) que acompanha a área de trabalho ativa — por isso funciona na tela de bloqueio, login e UAC, e envia Ctrl+Alt+Del de verdade (`SendSAS`).

## Modo navegador (alternativo)

O projeto também mantém o modo original: servidor de sinalização (`cmd/server`) + agente (`cmd/agent`) + visualizador web, com WebRTC P2P. Útil para dar suporte a quem não tem nada instalado: a pessoa roda o agente e você abre um link no navegador. Compile com `.\build.ps1 -Server https://seu.dominio`; a configuração de servidor/TURN está em `cmd/server/main.go`.

## Estrutura do código

```
cmd/telaremota/     executável único (CLI, painel, serviço, ponto fixo, agente, visualizador)
cmd/telaremota-hub/ ponto fixo para servidores Linux (systemd)
internal/hub/       ponto fixo: QUIC, pareamento, dispositivos, convites, UPnP, retransmissão
internal/tailnet/   rede Tailscale embutida (tsnet) e relay UDP local para os processos do usuário
internal/e2e/       criptografia de ponta a ponta entre visualizador e agente
internal/access/    senha de acesso (desafio PBKDF2/HMAC ligado à sessão TLS)
internal/identity/  chaves Ed25519, TLS mútuo e pinning
internal/host/      sessão no computador acessado (captura, entrada, troca de área de trabalho)
internal/viewer/    janela Win32 do visualizador
internal/winui/     controles Win32 mínimos do painel (cmd/telaremota/gui.go)
internal/service/   serviço do Windows e processo auxiliar na sessão do usuário
internal/wire/      protocolo binário da sessão
internal/auth/      PBKDF2/HMAC (convites e modo navegador)
internal/screen, input, clipboard   captura (DXGI/GDI), SendInput, área de transferência
cmd/server, cmd/agent, web/         modo navegador
scripts/                            diagnóstico de rede e teste da rede Tailscale embutida
```
