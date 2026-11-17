# TelaRemota

> **English summary:** TelaRemota is a lightweight remote desktop tool for Windows (written in Go) that connects your computers **directly, without third-party servers**. Sessions are end-to-end encrypted (QUIC + TLS 1.3, Ed25519 device keys with pinning). One machine acts as the rendezvous point ("ponto fixo"); it can be any PC with a public IP/UPnP, a Linux VPS, or — behind CGNAT on both sides — reachable through a **built-in Tailscale network** (no Tailscale install needed). The docs are in Brazilian Portuguese.

Acesso remoto leve, no estilo AnyDesk/TeamViewer, mas **direto entre os seus computadores — sem servidores de terceiros** e **cifrado de ponta a ponta**.

- Um único executável para Windows (`telaremota.exe`): painel gráfico, serviço, ponto de encontro e visualizador.
- Funciona na tela de bloqueio, no login e no UAC; envia Ctrl+Alt+Del de verdade.
- Várias telas, até 60 qps (captura pela GPU), teclado ABNT2, área de transferência de texto.
- Pareamento por código de convite + senha de acesso opcional por computador.

## Como funciona (em uma frase)

Um computador alcançável vira o **ponto fixo**; os outros se pareiam a ele uma vez e mantêm uma conexão de saída (que passa por CGNAT). A partir daí, qualquer computador pareado controla qualquer outro que permita acesso — nos dois sentidos, ou num só (modo técnico: [docs/tailscale.md](docs/tailscale.md#técnico-só-controlar-mão-única)).

```
 CASA (atrás de CGNAT)                         EMPRESA (ponto fixo)
 TelaRemota ─── conexão de saída (QUIC, UDP 47070) ───► TelaRemota
     ▲                                                       │
     └────── a empresa controla a casa pelo mesmo túnel ─────┘
```

## Baixar / compilar

**Pronto para usar:** baixe a última versão em [Releases](https://github.com/athosbes/telaremota/releases/latest):

- `telaremota-X.Y.Z-windows-amd64.zip`: Windows 10/11 (descompacte e dê duplo clique em `telaremota.exe`);
- `telaremota-hub-X.Y.Z-linux-amd64.tar.gz` / `-arm64`: ponto fixo em servidor Linux (opcional).

Confira a integridade com `SHA256SUMS.txt`. O executável não é assinado: se aparecer "O Windows protegeu o computador", clique em *Mais informações → Executar assim mesmo*.

**Compilar:** precisa do **Go 1.27.4 ou mais novo** (https://go.dev/dl/).

```bash
git clone https://github.com/athosbes/telaremota.git telaremota
cd telaremota
go build -trimpath -ldflags "-s -w" -o telaremota.exe ./cmd/telaremota
```

Ponto fixo para servidor Linux (opcional):

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o telaremota-hub ./cmd/telaremota-hub
```

Atalhos: `.\build.ps1` (PowerShell) ou `make` geram tudo em `dist/`.

## Começo rápido

Em todos os cenários: **dê duplo clique em `telaremota.exe`** (pede administrador). O painel tem três abas — *Conectar*, *Este computador* e *Ponto fixo*. Defina uma **senha de acesso** em cada computador (*Este computador → Definir senha*).

### (a) Um dos computadores tem IP público (ou UPnP no roteador)

1. **No computador com IP público** (ex.: empresa): *Este computador* → nome `empresa`, marque **Ser o ponto fixo** e **Permitir acesso** → **Salvar e iniciar serviço**. A porta UDP 47070 é aberta no roteador (UPnP) e no Firewall do Windows.
2. Aba *Ponto fixo* → **Gerar código de convite** → **Copiar** (uma linha: `endereço código`, vale 15 min).
3. **No outro computador** (ex.: casa): *Este computador* → nome `casa`, cole a linha em **Endereço do ponto fixo** → **Parear** → marque **Permitir acesso** → **Salvar e iniciar serviço**.
4. Aba *Conectar* → duplo clique no computador.

Não sabe se tem IP público? Rode `scripts\diagnostico-rede.ps1` (mostra CGNAT, IPv6, UPnP e tipo de NAT).

### (b) Casa ↔ empresa, os dois atrás de CGNAT/firewall — rede Tailscale embutida

Sem abrir portas e **sem instalar o Tailscale**: o TelaRemota já o traz embutido.

1. Crie uma conta gratuita em https://tailscale.com e gere uma *auth key* (reutilizável, de preferência com tag) em *Settings → Keys*.
2. Nos dois computadores, em *Este computador*: marque **Rede Tailscale embutida** e cole a chave em **Chave** → **Salvar e iniciar serviço**. Aparece "Conectado: 100.x.y.z".
3. Siga o cenário (a) normalmente: o convite já sai com o endereço 100.x.y.z.

Passo a passo completo: **[docs/tailscale.md](docs/tailscale.md)**.

### (c) Ponto fixo num servidor Linux na nuvem

Quando nenhum dos computadores pode ser o ponto fixo, use uma VPS barata ou gratuita. O `telaremota-hub` só repassa bytes cifrados: o servidor não vê tela, teclado nem área de transferência.

```bash
sudo install -m 755 telaremota-hub /usr/local/bin/
sudo telaremota-hub instalar -nome nuvem     # serviço systemd + firewall (libere UDP 47070 no provedor)
sudo telaremota-hub convite -admin           # código para parear os computadores
```

Depois, em cada computador: *Este computador* → endereço = IP do servidor + código → **Parear** → **Salvar e iniciar serviço** (sem marcar "Ser o ponto fixo"). Passo a passo (Oracle Cloud gratuita, vale para qualquer VPS): **[docs/oracle-cloud.md](docs/oracle-cloud.md)**.

## Na janela remota

**Ctrl+Alt+Enter** = tela cheia · **Ctrl+Alt+←/→** = trocar de tela · clique direito na barra de título = Ctrl+Alt+Del, qualidade, escolha de tela. As teclas Win, Alt+Tab e PrintScreen vão para o computador remoto.

## Segurança

| Camada | O que é |
|---|---|
| Transporte | **QUIC** (UDP 47070), TLS 1.3. Keep-alive mantém o mapeamento do CGNAT aberto. |
| Identidade | Cada instalação gera uma chave **Ed25519**; a impressão digital (SHA-256) identifica o dispositivo. |
| Autorização | **TLS mútuo**: o ponto fixo só aceita chaves pareadas; desconhecidos são recusados no primeiro pacote. O cliente fixa (*pinning*) a chave do ponto fixo. |
| Pareamento | Código de convite de uso limitado (padrão: 15 min, 4 usos, 5 erros invalidam). Prova HMAC(PBKDF2(código)) ligada às duas impressões digitais. |
| Ponta a ponta | Visualizador e agente fazem um TLS 1.3 próprio dentro do canal repassado; o ponto fixo não consegue decifrar a sessão. |
| Senha de acesso | Opcional, pedida a quem conecta. O agente guarda só PBKDF2(senha); a prova é ligada à sessão TLS; bloqueio progressivo após 3 erros. |

Encontrou uma falha de segurança? Veja [CONTRIBUTING.md](CONTRIBUTING.md#segurança).

## Limitações

- Windows 10/11 64 bits (o ponto fixo também roda em Linux).
- Se o ponto fixo estiver desligado, os outros não se encontram.
- Sem transferência de arquivos e sem áudio, por enquanto.
- Ainda não há aviso visual no computador acessado durante a sessão (só no log).

## Mais documentação

- [docs/linha-de-comando.md](docs/linha-de-comando.md) — instalação e administração pela linha de comando, arquivos de dados, modo navegador, estrutura do código.
- [docs/tailscale.md](docs/tailscale.md) — casa ↔ empresa com Tailscale.
- [docs/oracle-cloud.md](docs/oracle-cloud.md) — ponto fixo em servidor Linux.
- [CONTRIBUTING.md](CONTRIBUTING.md) — como compilar, testar e contribuir.

## Autor

Criado e mantido por [athosbes](https://github.com/athosbes) (athosbes@outlook.com). Veja também [AUTHORS](AUTHORS) e o [histórico de versões](CHANGELOG.md).

## Licença

Copyright (C) 2026 athosbes.


[GNU AGPL-3.0](LICENSE). Você pode usar, estudar, modificar e redistribuir o TelaRemota. Quem distribuir uma versão modificada, ou oferecê-la a outras pessoas pela rede (por exemplo, um ponto fixo como serviço), precisa disponibilizar o código-fonte dessa versão sob a mesma licença.

Os executáveis incluem bibliotecas de terceiros com licenças próprias, todas compatíveis (BSD-3-Clause, MIT, Apache-2.0), entre elas `tailscale.com`, `quic-go` e `pion`. Ao redistribuir os executáveis, inclua os avisos de licença dessas bibliotecas.
