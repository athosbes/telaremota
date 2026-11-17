# Ponto fixo na Oracle Cloud (gratuito)

> Este passo a passo usa a Oracle Cloud, mas vale para **qualquer servidor Linux** com IP público (VPS de qualquer provedor): o essencial é a Parte 3 (liberar UDP 47070) e a Parte 4 (instalar o `telaremota-hub`). Observação: no Brasil, a Oracle pode exigir CNPJ na criação da conta.

Uma máquina virtual "Always Free" da Oracle vira o ponto fixo. Casa e empresa só fazem **conexões de saída** para ela, o que funciona atrás de CGNAT e de firewall corporativo. A tela é cifrada de ponta a ponta: o servidor só repassa bytes e não consegue vê-la.

Tempo estimado: 20–40 minutos (a criação da conta pode levar mais).

---

## Parte 1 — Criar a conta

1. Acesse **https://signup.cloud.oracle.com**.
2. País: **Brasil**. Informe nome e e-mail e confirme o e-mail pelo link recebido.
3. Defina a senha e o **nome da conta de nuvem** (ex.: `meu-remoto`; é usado no login).
4. **Região inicial (Home Region): `Brazil East (Sao Paulo)`.**
   ⚠️ Essa escolha é permanente, e os recursos gratuitos só existem na região inicial.
5. Endereço e telefone.
6. Cartão de crédito: é só uma verificação de identidade. Pode aparecer uma cobrança temporária de valor simbólico, estornada em seguida. **Nada é cobrado** enquanto você usar apenas recursos "Always Free".
7. Aguarde o e-mail "Your account is ready" (de minutos até ~1 hora).

### Recomendado: evitar que a máquina seja recolhida

Em contas exclusivamente gratuitas, a Oracle pode **recuperar máquinas ociosas** (uso de CPU e rede muito baixo por 7 dias). Um ponto fixo fica ocioso quase o tempo todo. Para evitar isso:

**Menu ☰ → Billing & Cost Management → Upgrade and Manage Payment → Pay As You Go → Upgrade.**

Os recursos "Always Free" **continuam gratuitos** depois do upgrade. Para ter tranquilidade, crie um orçamento com alerta: **Billing → Budgets → Create Budget**, valor R$ 1, alerta por e-mail.

---

## Parte 2 — Criar a máquina virtual

1. **Menu ☰ → Compute → Instances → Create instance.**
2. **Name:** `telaremota`.
3. **Image and shape → Edit:**
   - **Image → Change image → Ubuntu →** `Canonical Ubuntu 24.04` → *Select image*.
   - **Shape → Change shape → Virtual machine → Specialty and previous generation →** **`VM.Standard.E2.1.Micro`** (selo "Always Free-eligible") → *Select shape*.
     - Se essa opção não estiver disponível, use **Ampere → `VM.Standard.A1.Flex`** com 1 OCPU e 6 GB (também gratuita; use o arquivo `-arm64` mais adiante).
4. **Networking:** deixe criar uma nova rede virtual (VCN) e uma **sub-rede pública**, com **"Assign a public IPv4 address" ligado**.
5. **Add SSH keys → Generate a key pair for me → Save private key.**
   Guarde o arquivo (ex.: `%USERPROFILE%\.ssh\oracle-telaremota.key`). Sem ele não é possível entrar na máquina.
6. **Create.** Aguarde o estado **Running** e anote o **Public IP address** (ex.: `150.230.x.x`).

---

## Parte 3 — Liberar a porta UDP 47070 na rede da Oracle

1. Na página da instância, aba **Networking** (ou "Primary VNIC") → clique no nome da **Subnet**.
2. Aba **Security** (ou "Security Lists") → **Default Security List for …**.
3. **Security rules / Ingress Rules → Add Ingress Rules:**
   - **Source CIDR:** `0.0.0.0/0`
   - **IP Protocol:** `UDP`
   - **Destination Port Range:** `47070`
   - **Description:** `TelaRemota`
4. **Add Ingress Rules.**

(O firewall interno do Ubuntu é liberado automaticamente pelo instalador na Parte 4.)

---

## Parte 4 — Instalar o ponto fixo

No **PowerShell do seu computador** (o Windows 10/11 já traz `ssh` e `scp`).

**1. Proteger a chave** (o SSH recusa chaves com permissões abertas):

```powershell
icacls "$env:USERPROFILE\.ssh\oracle-telaremota.key" /inheritance:r /grant:r "$($env:USERNAME):R"
```

**2. Enviar o programa** (troque `IP` pelo Public IP da máquina; use o arquivo `-arm64` se escolheu Ampere):

```powershell
scp -i "$env:USERPROFILE\.ssh\oracle-telaremota.key" dist\telaremota-hub-linux-amd64 ubuntu@IP:~/telaremota-hub
```

**3. Entrar na máquina:**

```powershell
ssh -i "$env:USERPROFILE\.ssh\oracle-telaremota.key" ubuntu@IP
```

**4. Já dentro da máquina, instalar e gerar o convite de administrador:**

```bash
sudo install -m 755 ~/telaremota-hub /usr/local/bin/telaremota-hub
sudo telaremota-hub instalar -nome nuvem
sudo telaremota-hub convite -admin
```

O `instalar` cria o serviço (systemd, inicia junto com a máquina) e libera a porta no firewall do Ubuntu. O `convite -admin` mostra um código válido por 15 minutos. Os usuários pareados com ele podem gerar convites e revogar dispositivos **pelo painel do Windows**, sem precisar voltar ao SSH.

---

## Parte 5 — Parear os computadores (no painel do TelaRemota)

Use o mesmo código nos dois computadores (ele vale para até 4 pareamentos).

Nos **dois** computadores (empresa e casa):

1. Duplo clique em `telaremota.exe` → UAC "Sim" → aba **Este computador**.
2. Nome: `empresa` (ou `casa`). Endereço do ponto fixo: `IP-da-Oracle`. Código: o do convite → **Parear**.
3. **Desmarque** "Ser o ponto fixo", mantenha **"Permitir acesso"** marcado → **Salvar e iniciar serviço** → UAC "Sim".

Na empresa, o passo 3 substitui a instalação anterior (que era ponto fixo local).

**Usar:** aba **Conectar** → duplo clique em `casa` ou `empresa`.

Para gerar novos convites ou revogar um dispositivo depois: aba **Ponto fixo** do painel, em qualquer computador cujo usuário foi pareado com o convite de administrador.

---

## Manutenção

| Ação | Comando (no SSH da Oracle) |
|---|---|
| Ver log | `sudo journalctl -u telaremota -f` |
| Estado | `sudo systemctl status telaremota` |
| Dispositivos | `sudo telaremota-hub dispositivos` |
| Revogar | `sudo telaremota-hub remover <nome>` |
| Atualizar | enviar o novo binário (passos 2–3), `sudo install …` e `sudo systemctl restart telaremota` |
| Desinstalar | `sudo telaremota-hub desinstalar` |
