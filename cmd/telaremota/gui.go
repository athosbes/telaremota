package main

// Painel gráfico de configuração (abre com duplo clique no executável ou
// "telaremota painel").

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"telaremota/internal/access"
	"telaremota/internal/clipboard"
	"telaremota/internal/config"
	"telaremota/internal/hub"
	"telaremota/internal/identity"
	"telaremota/internal/service"
	"telaremota/internal/tailnet"
	"telaremota/internal/winui"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	pGetConsoleProcs = kernel32.NewProc("GetConsoleProcessList")
	pFreeConsole     = kernel32.NewProc("FreeConsole")
)

// ownsConsole: o console foi criado só para este processo (aberto pelo Explorer).
func ownsConsole() bool {
	var ids [4]uint32
	n, _, _ := pGetConsoleProcs.Call(uintptr(unsafe.Pointer(&ids[0])), 4)
	return n == 1
}

func freeConsole() { pFreeConsole.Call() }

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

// cmdPanel abre o painel, pedindo elevação se necessário.
func cmdPanel() error {
	if !config.Overridden() && !service.IsElevated() {
		exe, _ := os.Executable()
		err := windows.ShellExecute(0, windows.StringToUTF16Ptr("runas"), windows.StringToUTF16Ptr(exe),
			windows.StringToUTF16Ptr("painel"), nil, windows.SW_SHOWNORMAL)
		if err != nil {
			winui.MessageBox("O painel do TelaRemota precisa de permissão de administrador para configurar o serviço.",
				"TelaRemota", winui.MBIconWarn)
		}
		return nil
	}
	runtime.LockOSThread()
	p := &panel{}
	return p.run()
}

type panel struct {
	a     *winui.App
	pages [3][]*winui.Ctl
	cur   int
	msg   *winui.Ctl

	online, onlineInfo *winui.Ctl
	listing            atomic.Bool

	status, name, chkHub, port, chkUPnP, chkAgent *winui.Ctl
	accessPw, accessInfo                          *winui.Ctl
	chkTailnet, tsKey, tsInfo                     *winui.Ctl
	btnInstall, btnRemove                         *winui.Ctl
	pairAddr, pairCode, btnPair                   *winui.Ctl

	hubNote, btnInvite, code, btnCopy, codeInfo, devices, btnRevoke, chkAdminInv *winui.Ctl
	loadingDevs                                                                  atomic.Bool
	inviteLine                                                                   string

	busy atomic.Bool
}

func (p *panel) add(page int, c *winui.Ctl) *winui.Ctl {
	p.pages[page] = append(p.pages[page], c)
	c.OnTab()
	return c
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func (p *panel) run() error {
	a, err := winui.New("TelaRemota — Painel", 700, 610)
	if err != nil {
		return err
	}
	p.a = a
	tabs := a.Tabs(10, 10, 680, 546, []string{"  Conectar  ", "  Este computador  ", "  Ponto fixo  "}, p.showPage)

	// ---- Aba 0: Conectar
	p.add(0, a.Label("Computadores online", 26, 48, 300, 20))
	p.online = p.add(0, a.ListBox(26, 72, 450, 330, p.connect))
	p.add(0, a.Button("Conectar", 492, 72, 180, 34, p.connect))
	p.add(0, a.Button("Atualizar lista", 492, 114, 180, 30, p.refreshOnline))
	p.add(0, a.Muted(crlf("Clique duas vezes em um computador para abrir a tela dele.\n\nNa janela remota:\n• Ctrl+Alt+Enter: tela cheia\n• Clique direito no título: Ctrl+Alt+Del e qualidade"), 492, 160, 182, 160))
	p.onlineInfo = p.add(0, a.Muted("", 26, 412, 646, 40))

	// ---- Aba 1: Este computador
	p.status = p.add(1, a.Label("", 26, 46, 646, 66))
	p.add(1, a.Label("Nome deste computador:", 26, 122, 170, 22))
	p.name = p.add(1, a.Edit(config.Hostname(), 200, 119, 200, 24, false))
	p.chkHub = p.add(1, a.Check("Ser o ponto fixo (recebe conexões da internet; precisa de IP público ou IPv6)", 26, 154, 640, 22, p.syncEnabled))
	p.add(1, a.Label("Porta UDP:", 52, 185, 80, 22))
	p.port = p.add(1, a.Edit(strconv.Itoa(config.DefaultPort), 132, 182, 70, 24, true))
	p.chkUPnP = p.add(1, a.Check("Abrir a porta no roteador automaticamente (UPnP)", 222, 184, 420, 22, nil))
	p.chkAgent = p.add(1, a.Check("Permitir que este computador seja acessado a qualquer momento (inclusive bloqueado)", 26, 216, 640, 22, nil))
	p.chkTailnet = p.add(1, a.Check("Rede Tailscale embutida (sem instalar o Tailscale)", 26, 248, 380, 22, p.syncEnabled))
	p.add(1, a.Label("Chave:", 52, 279, 50, 22))
	p.tsKey = p.add(1, a.Password(102, 276, 300, 24))
	p.tsInfo = p.add(1, a.Muted("", 412, 248, 262, 56))
	p.add(1, a.Label("Senha de acesso:", 52, 311, 120, 22))
	p.accessPw = p.add(1, a.Password(172, 308, 180, 24))
	p.add(1, a.Button("Definir senha", 362, 306, 120, 28, p.setPassword))
	p.accessInfo = p.add(1, a.Muted("", 492, 311, 182, 22))
	p.btnInstall = p.add(1, a.Button("Salvar e iniciar serviço", 26, 346, 210, 34, p.install))
	p.btnRemove = p.add(1, a.Button("Remover serviço", 246, 346, 150, 34, p.remove))
	p.add(1, a.Label("Parear com um ponto fixo", 26, 396, 400, 22)).SetFont(a.Font("Segoe UI", 9, true))
	p.add(1, a.Label("Endereço do ponto fixo:", 26, 427, 170, 22))
	p.pairAddr = p.add(1, a.Edit("", 200, 424, 270, 24, false))
	p.add(1, a.Label("Código do convite:", 26, 460, 170, 22))
	p.pairCode = p.add(1, a.Edit("", 200, 457, 150, 24, false))
	p.btnPair = p.add(1, a.Button("Parear", 492, 424, 180, 57, p.pair))
	p.add(1, a.Muted(crlf("Cole aqui o convite gerado na aba \"Ponto fixo\" do computador principal (a linha inteira\n\"endereço código\" pode ser colada no campo de endereço). Não é necessário no próprio ponto fixo."), 26, 494, 646, 44))

	// ---- Aba 2: Ponto fixo
	p.hubNote = p.add(2, a.Label("", 26, 46, 646, 40))
	p.btnInvite = p.add(2, a.Button("Gerar código de convite", 26, 92, 200, 36, p.invite))
	p.code = p.add(2, a.Label("", 244, 90, 230, 40))
	p.code.SetFont(a.Font("Consolas", 20, true))
	p.btnCopy = p.add(2, a.Button("Copiar", 484, 94, 90, 32, p.copyCode))
	p.chkAdminInv = p.add(2, a.Check("Convite de administrador (o usuário pareado poderá gerar convites e revogar)", 26, 136, 640, 22, nil))
	p.codeInfo = p.add(2, a.Muted("", 26, 162, 646, 44))
	p.add(2, a.Label("Dispositivos pareados", 26, 214, 300, 20))
	p.devices = p.add(2, a.ListBox(26, 238, 450, 200, nil))
	p.btnRevoke = p.add(2, a.Button("Revogar acesso", 492, 238, 180, 34, p.revoke))
	p.add(2, a.Button("Atualizar lista", 492, 278, 180, 30, p.refreshDevices))
	p.add(2, a.Muted(crlf("O dispositivo revogado é desconectado em até 10 segundos e não consegue mais entrar."), 492, 318, 182, 80))

	p.msg = a.Label("", 14, 566, 672, 38)
	tabs.SendToBack()

	p.loadForm()
	p.refreshStatus()
	p.refreshAccess()
	p.showPage(0)
	start := 0
	if !config.Exists(userPath("client.json")) {
		start = 1
	}
	if n, err := strconv.Atoi(os.Getenv("TELAREMOTA_ABA")); err == nil && config.Overridden() {
		start = n // apenas em modo de teste
	}
	if start != 0 {
		p.showPage(start)
		winSetTab(tabs, start)
	}
	// No próprio ponto fixo, o usuário que abre o painel é pareado
	// automaticamente (como administrador) para poder controlar os outros.
	if m, ok := localHubActive(); ok && !config.Exists(userPath("client.json")) {
		p.work("Pareando seu usuário ao ponto fixo local…", func() (string, error) {
			cl, err := pairLocalUser(m.Port)
			if err != nil {
				return "", fmt.Errorf("não foi possível parear seu usuário: %w", err)
			}
			p.a.Do(func() {
				p.showPage(0)
				winSetTab(tabs, 0)
			})
			return fmt.Sprintf("Seu usuário foi pareado ao ponto fixo local como %q.", cl.Name), nil
		})
	}
	p.refreshOnline()
	a.Every(5000, func() {
		if p.cur == 0 {
			p.refreshOnline()
		}
	})
	a.Every(3000, func() {
		if !p.busy.Load() {
			p.refreshStatus()
		}
	})
	a.Run()
	return nil
}

func winSetTab(tabs *winui.Ctl, i int) {
	// TCM_SETCURSEL
	windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageW").Call(tabs.Hwnd, 0x130C, uintptr(i), 0)
}

func (p *panel) showPage(i int) {
	p.cur = i
	for pg, ctls := range p.pages {
		for _, c := range ctls {
			c.Show(pg == i)
		}
	}
	if i == 2 {
		p.refreshDevices()
	}
	if i == 0 {
		p.refreshOnline()
	}
}

func (p *panel) say(format string, args ...any) { p.msg.SetText(crlf(fmt.Sprintf(format, args...))) }

// work executa f fora da thread da interface, bloqueando cliques repetidos.
func (p *panel) work(start string, f func() (string, error)) {
	if !p.busy.CompareAndSwap(false, true) {
		return
	}
	p.say("%s", start)
	for _, c := range []*winui.Ctl{p.btnInstall, p.btnRemove, p.btnPair, p.btnInvite, p.btnRevoke} {
		c.Enable(false)
	}
	go func() {
		ok, err := f()
		p.a.Do(func() {
			p.busy.Store(false)
			for _, c := range []*winui.Ctl{p.btnInstall, p.btnRemove, p.btnPair, p.btnInvite, p.btnRevoke} {
				c.Enable(true)
			}
			if err != nil {
				p.say("Erro: %v", err)
			} else {
				p.say("%s", ok)
			}
			p.refreshStatus()
			p.syncEnabled()
		})
	}()
}

// ---------------------------------------------------------------- estado

func (p *panel) loadForm() {
	var m config.Machine
	if config.Load(machinePath("machine.json"), &m) == nil {
		if m.Name != "" {
			p.name.SetText(m.Name)
		}
		if m.Port != 0 {
			p.port.SetText(strconv.Itoa(m.Port))
		}
		p.chkHub.SetChecked(m.Hub)
		p.chkUPnP.SetChecked(m.UPnP || !m.Hub)
		p.chkAgent.SetChecked(m.Agent)
		p.chkTailnet.SetChecked(m.Tailnet)
	} else {
		p.chkUPnP.SetChecked(true)
		p.chkAgent.SetChecked(true)
	}
	var cl config.Client
	if config.Load(machinePath("client.json"), &cl) == nil {
		p.pairAddr.SetText(cl.Hub)
		if cl.Name != "" && m.Name == "" {
			p.name.SetText(cl.Name)
		}
	}
	p.syncEnabled()
}

func (p *panel) syncEnabled() {
	hubOn := p.chkHub.Checked()
	p.port.Enable(hubOn)
	p.chkUPnP.Enable(hubOn)
	p.tsKey.Enable(p.chkTailnet.Checked())
	_, err := adminFor()
	for _, c := range []*winui.Ctl{p.btnInvite, p.btnCopy, p.devices, p.btnRevoke, p.chkAdminInv} {
		c.Enable(err == nil && !p.busy.Load())
	}
}

func (p *panel) refreshStatus() {
	state := serviceState()
	var m config.Machine
	hasM := config.Load(machinePath("machine.json"), &m) == nil
	var mc, uc config.Client
	hasMC := config.Load(machinePath("client.json"), &mc) == nil
	hasUC := config.Load(userPath("client.json"), &uc) == nil

	var b strings.Builder
	fmt.Fprintf(&b, "Serviço: %s", state)
	if hasM && state != "não instalado" {
		var roles []string
		if m.Hub {
			roles = append(roles, fmt.Sprintf("ponto fixo (UDP %d)", m.Port))
		}
		if m.Agent {
			roles = append(roles, "acesso permitido")
		}
		if !m.Hub && !m.Agent {
			roles = append(roles, "só controla outros (não aceita acesso)")
		}
		fmt.Fprintf(&b, "  ·  %s  ·  nome %q", strings.Join(roles, " + "), m.Name)
	}
	b.WriteString("\n")
	switch {
	case hasM && m.Hub && state != "não instalado":
		b.WriteString("Este computador é o ponto fixo.\n")
	case hasMC:
		fmt.Fprintf(&b, "Computador pareado como %q ao ponto fixo %q (%s).\n", mc.Name, mc.HubName, mc.Hub)
	default:
		b.WriteString("Computador ainda não pareado a um ponto fixo.\n")
	}
	if hasUC {
		fmt.Fprintf(&b, "Seu usuário: %q — pode controlar outros computadores.", uc.Name)
	} else {
		b.WriteString("Seu usuário ainda não pode controlar outros computadores (pareie abaixo).")
	}
	p.status.SetText(crlf(b.String()))
	p.tsInfo.SetText(crlf(tailnetInfo(hasM && m.Tailnet && state != "não instalado")))

	var st hub.Status
	if hasM && m.Hub && state != "não instalado" {
		config.Load(hub.StatusPath(config.MachineDir()), &st)
		note := fmt.Sprintf("Ponto fixo %q na porta UDP %d", m.Name, m.Port)
		if st.UPnP {
			note += fmt.Sprintf(" · porta aberta no roteador (IP público %s)", st.PublicIP)
		} else if m.UPnP {
			note += " · UPnP indisponível: libere a porta manualmente no roteador"
		}
		if ts := tailnet.LoadStatus(config.MachineDir()); m.Tailnet && ts.Running() {
			note += "\nEndereço na rede Tailscale (embutida): " + ts.IP + "  (use este nos outros computadores)"
		} else if _, _, vpn := hub.LocalAddrs(); len(vpn) > 0 {
			note += "\nEndereço na rede Tailscale/ZeroTier: " + vpn[0] + "  (use este nos outros computadores)"
		} else if len(st.IPv6) > 0 {
			note += "\nIPv6: " + st.IPv6[0]
		}
		p.hubNote.SetText(crlf(note))
	} else if hasUC {
		p.hubNote.SetText(crlf(fmt.Sprintf("Ponto fixo remoto %q (%s).\nConvites e revogações exigem que seu usuário tenha sido pareado com um convite de administrador.", uc.HubName, uc.Hub)))
	} else {
		p.hubNote.SetText(crlf("Este computador não é o ponto fixo e seu usuário não está pareado a um.\nPareie na aba \"Este computador\"."))
	}
}

// ---------------------------------------------------------------- ações

func (p *panel) refreshOnline() {
	if !p.listing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer p.listing.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		l, err := listOnline(ctx)
		cancel()
		p.a.Do(func() {
			if err != nil {
				p.online.SetItems(nil)
				p.onlineInfo.SetText(crlf(fmt.Sprint(err)))
				return
			}
			p.online.SetItems(l.Agents)
			p.onlineInfo.SetText(fmt.Sprintf("Ponto fixo %q (%s) · você é %q · %d online", l.Hub, l.HubAddr, l.You, len(l.Agents)))
		})
	}()
}

func (p *panel) connect() {
	name := p.online.Selected()
	if name == "" {
		p.say("Selecione um computador na lista.")
		return
	}
	if err := launchViewer(name); err != nil {
		p.say("Erro ao abrir o visualizador: %v", err)
		return
	}
	p.say("Abrindo a tela de %q…", name)
}

func (p *panel) form() (config.Machine, error) {
	port, err := strconv.Atoi(strings.TrimSpace(p.port.Text()))
	if err != nil || port < 1 || port > 65535 {
		return config.Machine{}, fmt.Errorf("porta inválida")
	}
	return config.Machine{
		Hub:     p.chkHub.Checked(),
		Port:    port,
		UPnP:    p.chkUPnP.Checked(),
		Name:    config.CleanName(p.name.Text()),
		Agent:   p.chkAgent.Checked(),
		Tailnet: p.chkTailnet.Checked(),
	}, nil
}

func (p *panel) install() {
	m, err := p.form()
	if err != nil {
		p.say("Erro: %v", err)
		return
	}
	key := p.tsKey.Text()
	p.work("Instalando e iniciando o serviço…", func() (string, error) {
		if m.Tailnet {
			if err := tailnet.SaveAuthKey(config.MachineDir(), key); err != nil {
				return "", err
			}
			p.a.Do(func() { p.tsKey.SetText("") })
		}
		if _, err := installService(m); err != nil {
			return "", err
		}
		msg := "Serviço instalado e em execução."
		if m.Hub && !config.Exists(userPath("client.json")) {
			if _, err := pairLocalUser(m.Port); err != nil {
				return "", fmt.Errorf("serviço instalado, mas não foi possível parear seu usuário: %w", err)
			}
			msg += " Seu usuário foi pareado ao ponto fixo local."
		}
		if !m.Hub && !m.Agent {
			msg += " Este computador só controla outros: ninguém consegue acessá-lo."
		}
		if m.Hub {
			msg += " Gere convites na aba \"Ponto fixo\"."
		}
		return msg, nil
	})
}

func (p *panel) remove() {
	if p.a.Message("Remover o serviço? Este computador deixará de ser acessível e, se for o ponto fixo, os outros não se conectarão.",
		"TelaRemota", winui.MBYesNo|winui.MBIconQ) != winui.IDYes {
		return
	}
	p.work("Removendo o serviço…", func() (string, error) {
		if err := service.Uninstall(); err != nil {
			return "", err
		}
		return "Serviço removido. Os pareamentos foram mantidos.", nil
	})
}

func (p *panel) pair() {
	addr, code, name := p.pairAddr.Text(), p.pairCode.Text(), p.name.Text()
	if strings.TrimSpace(addr) == "" || (strings.TrimSpace(code) == "" && len(strings.Fields(addr)) != 2) {
		p.say("Cole o convite (\"endereço código\") no campo Endereço, ou preencha endereço e código.")
		return
	}
	keepRole := p.chkTailnet.Checked() // técnico: mantém a escolha de (não) permitir acesso
	viewOnly := keepRole && !p.chkAgent.Checked() && !p.chkHub.Checked()
	p.work("Pareando…", func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := pairAll(ctx, addr, code, name, true)
		if err != nil {
			return "", err
		}
		p.a.Do(func() {
			p.pairCode.SetText("")
			if !keepRole {
				p.chkAgent.SetChecked(true)
			}
		})
		out := res.MachineMsg + "\nUsuário pareado como " + strconv.Quote(res.User.Name) + "."
		if viewOnly {
			out += " Pronto: veja os computadores na aba \"Conectar\"."
		} else if keepRole {
			out += " Clique em \"Salvar e iniciar serviço\" de novo para o agente se registrar."
		} else if res.Machine != nil {
			out += " Clique em \"Salvar e iniciar serviço\" para permitir o acesso a este computador."
		}
		return out, nil
	})
}

func (p *panel) invite() {
	asAdmin := p.chkAdminInv.Checked()
	p.work("Gerando convite…", func() (string, error) {
		a, err := adminFor()
		if err != nil {
			return "", err
		}
		code, addr, fp, err := a.Invite(4, 15*time.Minute, asAdmin)
		if err != nil {
			return "", err
		}
		kind := ""
		if asAdmin {
			kind = " de ADMINISTRADOR"
		}
		line := addr + " " + code
		info := fmt.Sprintf("Convite%s válido por 15 min, até 4 pareamentos. Clique em Copiar e cole no outro computador\n(painel → \"Este computador\" → campo Endereço):   %s      [ponto fixo %s]",
			kind, line, identity.Short(fp))
		p.a.Do(func() {
			p.inviteLine = line
			p.code.SetText(code)
			p.codeInfo.SetText(crlf(info))
			p.chkAdminInv.SetChecked(false)
		})
		return "Convite gerado no " + a.Where() + ".", nil
	})
}

func (p *panel) copyCode() {
	if p.inviteLine != "" {
		clipboard.Set(p.inviteLine)
		p.say("Convite copiado: %s — cole no campo Endereço do outro computador.", p.inviteLine)
	}
}

func (p *panel) refreshDevices() {
	if !p.loadingDevs.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer p.loadingDevs.Store(false)
		var list []hub.Device
		a, err := adminFor()
		if err == nil {
			list, err = a.Devices()
		}
		p.a.Do(func() {
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, "não é administrador") {
					msg = "lista disponível só para administradores do ponto fixo"
				}
				p.devices.SetItems([]string{"(" + msg + ")"})
				return
			}
			items := make([]string, 0, len(list))
			for _, d := range list {
				kind := "computador"
				if d.Kind == "usuario" {
					kind = "usuário"
				}
				if d.Admin {
					kind += ", administrador"
				}
				items = append(items, fmt.Sprintf("%s — %s — %s", d.Name, kind, d.Added.Format("02/01/2006")))
			}
			p.devices.SetItems(items)
		})
	}()
}

func (p *panel) revoke() {
	sel := p.devices.Selected()
	if sel == "" || strings.HasPrefix(sel, "(") {
		p.say("Selecione um dispositivo.")
		return
	}
	name := strings.SplitN(sel, " — ", 2)[0]
	question := fmt.Sprintf("Revogar o acesso de %q?", name)
	var me config.Client
	if config.Load(userPath("client.json"), &me) == nil && me.Name == name {
		question = fmt.Sprintf("%q é o SEU usuário neste computador.\n\nSe revogar, você não conseguirá mais conectar a outros computadores a partir daqui (até parear de novo).\n\nRevogar mesmo assim?", name)
	}
	if p.a.Message(question, "TelaRemota", winui.MBYesNo|winui.MBIconWarn) != winui.IDYes {
		return
	}
	p.work("Revogando…", func() (string, error) {
		a, err := adminFor()
		if err != nil {
			return "", err
		}
		if err := a.Revoke(name); err != nil {
			return "", err
		}
		p.a.Do(p.refreshDevices)
		return fmt.Sprintf("Acesso de %q revogado.", name), nil
	})
}

func (p *panel) refreshAccess() {
	if _, ok := access.Load(config.MachineDir()); ok {
		p.accessInfo.SetText("✓ senha definida")
	} else {
		p.accessInfo.SetText("sem senha (só pareamento)")
	}
}

func (p *panel) setPassword() {
	pw := p.accessPw.Text()
	if pw == "" {
		if _, ok := access.Load(config.MachineDir()); !ok {
			p.say("Digite a senha de acesso que será pedida a quem conectar a este computador.")
			return
		}
		if p.a.Message("Remover a senha de acesso? Dispositivos pareados poderão conectar sem senha.",
			"TelaRemota", winui.MBYesNo|winui.MBIconQ) != winui.IDYes {
			return
		}
	}
	if err := access.Set(config.MachineDir(), pw); err != nil {
		p.say("Erro: %v", err)
		return
	}
	p.accessPw.SetText("")
	p.refreshAccess()
	if pw == "" {
		p.say("Senha de acesso removida.")
	} else {
		p.say("Senha de acesso definida. Ela será pedida a quem conectar a este computador (vale na hora, sem reiniciar o serviço).")
	}
}

// tailnetInfo descreve o estado do nó Tailscale embutido.
func tailnetInfo(on bool) string {
	if !on {
		return "Entra na sua rede Tailscale pelo próprio TelaRemota. Chave: login.tailscale.com → Settings → Keys (só no 1º uso)."
	}
	st := tailnet.LoadStatus(config.MachineDir())
	switch {
	case st.Running():
		return fmt.Sprintf("Conectado: %s\n%s", st.IP, st.DNSName)
	case st.AuthURL != "":
		return "Precisa de login: informe uma chave e salve de novo, ou abra:\n" + st.AuthURL
	case st.Error != "":
		return "Erro: " + st.Error
	case st.State != "":
		return "Rede Tailscale: " + st.State + "…"
	}
	return "Iniciando a rede Tailscale…"
}
