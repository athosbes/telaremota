# TelaRemota com Tailscale (casa ↔ empresa, sem abrir portas)

Com CGNAT em casa e firewall na empresa, nenhum dos lados aceita conexões de fora. O Tailscale cria uma rede privada entre os seus computadores que funciona mesmo assim, e o TelaRemota roda por cima dela:

- a **empresa** é o ponto fixo;
- a **casa** se conecta a ela pelo endereço Tailscale (`100.x.y.z`);
- a sessão continua **cifrada de ponta a ponta** pelo TelaRemota e protegida pela **senha de acesso** de cada computador.

Há dois jeitos. O recomendado é a **rede Tailscale embutida**: o `telaremota.exe` já traz o Tailscale dentro dele (biblioteca `tsnet`), então não é preciso instalar nada além do TelaRemota.

Tempo: ~10 minutos.

---

## Opção 1 (recomendada): rede Tailscale embutida

### 1. Conta e chave do Tailscale (uma vez)

1. Acesse **https://tailscale.com** → *Get started* → crie uma conta ou entre com a sua conta (Google, Microsoft, GitHub…). O plano **Personal** é gratuito.
2. Em **https://login.tailscale.com/admin/settings/keys** → *Generate auth key*. Marque **Reusable** e, de preferência, uma **tag** (ex.: `tag:telaremota`, definida antes em *Access controls*). Nós com tag não expiram a cada 180 dias.
3. Copie a chave (`tskey-auth-…`). Trate-a como uma senha: quem a tiver pode colocar computadores na sua rede.

### 2. Empresa (ponto fixo)

1. Duplo clique em `telaremota.exe` → UAC **Sim** → aba **Este computador**.
2. Nome `empresa`, marque **Ser o ponto fixo** e **Permitir acesso**.
3. Marque **Rede Tailscale embutida** e cole a chave no campo **Chave**.
4. **Senha de acesso:** digite uma senha forte → **Definir senha**.
5. **Salvar e iniciar serviço** → UAC **Sim**. Ao lado da opção aparece "Conectado: 100.x.y.z".
6. Aba **Ponto fixo** → **Gerar código de convite** → **Copiar**.
   Isso copia uma linha só, no formato `100.x.y.z abc-def-ghj`. O convite vale **15 minutos**.

### 3. Casa

1. Copie o `telaremota.exe` para o computador de casa e dê duplo clique → UAC **Sim**.
   Se aparecer "O Windows protegeu o computador": *Mais informações → Executar assim mesmo*.
2. Aba **Este computador** → Nome `casa`. Deixe **desmarcado** "Ser o ponto fixo" e **marcado** "Permitir acesso".
3. Marque **Rede Tailscale embutida**, cole a chave e clique em **Salvar e iniciar serviço** — a rede embutida precisa estar ativa **antes** de parear, porque o pareamento passa por ela.
4. Cole a linha do convite no campo **Endereço do ponto fixo** → **Parear**.
5. **Senha de acesso:** digite uma senha para a casa → **Definir senha**.
6. **Salvar e iniciar serviço** de novo.

Pela linha de comando (terminal como administrador):

```powershell
telaremota servico instalar -ponto-fixo -agente -nome empresa -tailnet -tailnet-chave tskey-auth-...   # empresa
telaremota servico instalar -agente -nome casa -tailnet -tailnet-chave tskey-auth-...                  # casa
```

Como funciona: o serviço do TelaRemota entra na sua rede Tailscale sozinho, e cada computador aparece uma vez no painel do Tailscale. Não é preciso marcar "Run unattended" nem fazer login no Tailscale. O painel e o visualizador do usuário alcançam a rede pelo serviço (relay local em `127.0.0.1:47071`). A chave é apagada do disco depois do primeiro login. Sem chave, o painel mostra um link de login.

### Técnico: só controlar (mão única)

Para um computador que só controla os outros e não pode ser acessado (ex.: notebook de um técnico de manutenção):

1. Aba **Este computador** → marque **Rede Tailscale embutida** e cole a chave. Deixe **desmarcados** "Ser o ponto fixo" e "Permitir acesso".
2. **Salvar e iniciar serviço**: o serviço só leva este computador à rede Tailscale, para o painel e o visualizador alcançarem o ponto fixo.
3. Cole o convite → **Parear**. Pronto: os computadores aparecem na aba **Conectar**.

Linha de comando: `telaremota servico instalar -tailnet -tailnet-chave tskey-auth-...` (sem `-agente`).

O técnico não precisa de conta no Tailscale: recebe só a chave e um convite. Gere um convite por pessoa, assim cada uma aparece com seu nome em **Dispositivos pareados** e pode ser revogada separadamente.

---

## Opção 2: Tailscale instalado

Se você já usa o aplicativo do Tailscale, o TelaRemota funciona por cima dele sem configuração extra.

1. Instale o Tailscale para Windows (**https://tailscale.com/download**) e entre **com a mesma conta** nos dois computadores.
2. **Acesso autônomo** (importante): no ícone do Tailscale perto do relógio → **Preferences** → marque **Run unattended**. Assim o Tailscale continua conectado com a tela bloqueada ou sem ninguém logado.
3. No painel web do Tailscale (**https://login.tailscale.com/admin/machines**), em cada computador: **⋯ → Disable key expiry**. Sem isso, a cada 180 dias o Tailscale pede login de novo e o acesso autônomo para.
4. Siga os passos 2 e 3 da Opção 1, **sem** marcar "Rede Tailscale embutida". O painel detecta o endereço 100.x do Tailscale instalado e o usa no convite.

---

## Usar

- Aba **Conectar** → duplo clique em `empresa` (estando em casa) ou em `casa` (estando na empresa).
- A senha de acesso daquele computador é pedida. Marque **"Lembrar a senha neste computador"** para não digitar de novo (fica cifrada pelo Windows, só para o seu usuário).
- Na janela remota: **Ctrl+Alt+Enter** = tela cheia; clique direito no título = **Ctrl+Alt+Del**, qualidade e escolha de tela.

---

## Problemas comuns

| Sintoma | O que fazer |
|---|---|
| "não foi possível conectar a 100.x…" | Confira se os dois computadores aparecem conectados no painel do Tailscale e se a empresa está ligada. |
| A casa some da lista quando ninguém está logado | Opção 2: ative **Run unattended** no Tailscale da casa. (A Opção 1 não tem esse problema.) |
| Parou de funcionar depois de meses | Opção 1: use uma chave com **tag**. Opção 2: faltou **Disable key expiry**. |
| "senha de acesso incorreta" várias vezes | Após 3 erros há um bloqueio temporário (10 s, 20 s, 40 s…). Espere e tente de novo. |
| "a chave de … mudou" | Aquele computador foi reinstalado e pareado de novo. Apague a linha dele em `%APPDATA%\TelaRemota\known_peers.json`. |
