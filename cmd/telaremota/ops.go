package main

// Operações compartilhadas entre a linha de comando e o painel gráfico.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"telaremota/internal/config"
	"telaremota/internal/hub"
	"telaremota/internal/identity"
	"telaremota/internal/service"
	"telaremota/internal/tailnet"
	"telaremota/internal/wire"
)

type pairResult struct {
	Machine    *config.Client // nil se não pareou a máquina
	MachineMsg string
	User       *config.Client
}

// pairAll pareia a máquina (se withMachine) e o usuário atual com o ponto fixo.
func pairAll(ctx context.Context, addrIn, code, name string, withMachine bool) (*pairResult, error) {
	// Aceita a linha inteira do convite ("endereço código") no campo de endereço.
	if f := strings.Fields(addrIn); len(f) == 2 && strings.TrimSpace(code) == "" {
		addrIn, code = f[0], f[1]
	}
	addr := hub.Addr(strings.TrimSpace(addrIn))
	code = strings.TrimSpace(code)
	n := config.CleanName(name)
	res := &pairResult{}
	if withMachine {
		mid, err := identity.LoadOrCreate(config.MachineDir())
		if err != nil {
			return nil, err
		}
		cl, err := hub.Pair(ctx, mid, addr, code, n, "maquina")
		switch {
		case err != nil && strings.Contains(err.Error(), "já está pareado"):
			res.MachineMsg = "Computador: já autorizado neste ponto fixo."
		case err != nil:
			return nil, err
		default:
			if err := config.Save(machinePath("client.json"), cl); err != nil {
				return nil, err
			}
			res.Machine = cl
			res.MachineMsg = fmt.Sprintf("Computador pareado como %q ao ponto fixo %q (%s).", cl.Name, cl.HubName, identity.Short(cl.HubFP))
		}
	}
	uid, err := identity.LoadOrCreate(config.UserDir())
	if err != nil {
		return nil, err
	}
	cl, err := hub.Pair(ctx, uid, addr, code, n+"-"+config.CleanName(os.Getenv("USERNAME")), "usuario")
	if err != nil {
		return res, fmt.Errorf("pareamento do usuário: %w", err)
	}
	if err := config.Save(userPath("client.json"), cl); err != nil {
		return res, err
	}
	res.User = cl
	return res, nil
}

// installService valida, grava machine.json e instala/reinicia o serviço.
func installService(m config.Machine) (string, error) {
	// Só com a rede Tailscale embutida o serviço apenas leva este computador
	// à rede, para controlar outros (mão única: ninguém o controla).
	if !m.Hub && !m.Agent && !m.Tailnet {
		return "", errors.New("escolha ao menos uma função: ponto fixo, permitir acesso ou rede Tailscale embutida")
	}
	if m.Agent && !m.Hub && !m.Tailnet && !config.Exists(machinePath("client.json")) {
		return "", errors.New("pareie este computador a um ponto fixo antes de permitir o acesso")
	}
	if m.Port == 0 {
		m.Port = config.DefaultPort
	}
	m.Name = config.CleanName(m.Name)
	if _, err := identity.LoadOrCreate(config.MachineDir()); err != nil {
		return "", err
	}
	if err := config.Save(machinePath("machine.json"), m); err != nil {
		return "", err
	}
	fw := 0
	if m.Hub {
		fw = m.Port
	}
	return service.Install(config.MachineDir(), fw)
}

// pairLocalUser pareia o usuário atual ao ponto fixo desta própria máquina
// (para controlar outros computadores a partir dela). Espera o serviço subir.
func pairLocalUser(port int) (*config.Client, error) {
	code, err := hub.NewInvite(config.MachineDir(), 1, 2*time.Minute, true)
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var last error
	for i := 0; i < 15; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res, err := pairAll(ctx, addr, code, config.Hostname(), false)
		cancel()
		if err == nil {
			return res.User, nil
		}
		last = err
		if !strings.Contains(err.Error(), "conectar") {
			break
		}
		time.Sleep(time.Second)
	}
	return nil, last
}

type onlineList struct {
	Hub, HubAddr, You string
	Agents            []string
}

func listOnline(ctx context.Context) (*onlineList, error) {
	id, cl, err := userClient()
	if err != nil {
		return nil, err
	}
	c, err := hub.Dial(ctx, id, cl)
	if err != nil {
		return nil, err
	}
	defer c.CloseWithError(0, "")
	var res struct {
		wire.Reply
		Agents []string `json:"agents"`
		You    string   `json:"you"`
		Hub    string   `json:"hub"`
	}
	if _, _, err := hub.Request(ctx, c, map[string]any{"op": "list"}, &res); err != nil {
		return nil, err
	}
	return &onlineList{Hub: res.Hub, HubAddr: cl.Hub, You: res.You, Agents: res.Agents}, nil
}

// serviceState descreve o estado do serviço do Windows.
func serviceState() string {
	m, err := mgr.Connect()
	if err != nil {
		return "desconhecido"
	}
	defer m.Disconnect()
	s, err := m.OpenService(service.Name)
	if err != nil {
		return "não instalado"
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "desconhecido"
	}
	switch st.State {
	case svc.Running:
		return "em execução"
	case svc.Stopped:
		return "parado"
	case svc.StartPending:
		return "iniciando"
	case svc.StopPending:
		return "parando"
	}
	return "instalado"
}

// launchViewer abre o visualizador em um processo separado.
func launchViewer(name string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "conectar", name)
	hideConsole(cmd)
	cmd.Env = append(os.Environ(), "TELAREMOTA_GUI=1")
	return cmd.Start()
}

// ---------------------------------------------------------------- administração

// adminAPI gera convites e gerencia dispositivos — no ponto fixo local
// (arquivos da máquina) ou remoto (usuário administrador, via rede).
type adminAPI interface {
	Where() string
	Invite(uses int, ttl time.Duration, admin bool) (code, addr, fp string, err error)
	Devices() ([]hub.Device, error)
	Revoke(name string) error
}

// localHubActive informa se este computador está configurado como ponto fixo.
func localHubActive() (config.Machine, bool) {
	var m config.Machine
	if config.Load(machinePath("machine.json"), &m) != nil || !m.Hub {
		return m, false
	}
	return m, config.Overridden() || serviceState() != "não instalado"
}

func adminFor() (adminAPI, error) {
	if m, ok := localHubActive(); ok {
		return localAdmin{m}, nil
	}
	id, cl, err := userClient()
	if err != nil {
		return nil, errors.New("este computador não é o ponto fixo e seu usuário não está pareado a um")
	}
	return remoteAdmin{id, cl}, nil
}

type localAdmin struct{ m config.Machine }

func (l localAdmin) Where() string { return fmt.Sprintf("ponto fixo local %q", l.m.Name) }

func (l localAdmin) Invite(uses int, ttl time.Duration, admin bool) (string, string, string, error) {
	code, err := hub.NewInvite(config.MachineDir(), uses, ttl, admin)
	if err != nil {
		return "", "", "", err
	}
	var st hub.Status
	config.Load(hub.StatusPath(config.MachineDir()), &st)
	return code, hostPort(bestAddr(st), st.Port), st.FP, nil
}

// bestAddr escolhe o endereço que os outros computadores devem usar:
// IP público (UPnP) → rede Tailscale/ZeroTier → IPv6 → rede local.
func bestAddr(st hub.Status) string {
	v6, lan, vpn := hub.LocalAddrs() // consulta ao vivo (o Tailscale pode ter sido instalado depois)
	ts := tailnet.LoadStatus(config.MachineDir())
	switch {
	case st.PublicIP != "":
		return st.PublicIP
	case ts.Running():
		return ts.IP
	case len(vpn) > 0:
		return vpn[0]
	case len(v6) > 0:
		return v6[0]
	case len(lan) > 0:
		return lan[0]
	}
	return "<endereço deste computador>"
}

func (l localAdmin) Devices() ([]hub.Device, error) { return hub.ListDevices(config.MachineDir()) }
func (l localAdmin) Revoke(name string) error       { return hub.RemoveDevice(config.MachineDir(), name) }

type remoteAdmin struct {
	id *identity.Identity
	cl *config.Client
}

func (r remoteAdmin) Where() string { return fmt.Sprintf("ponto fixo %q (%s)", r.cl.HubName, r.cl.Hub) }

func (r remoteAdmin) call(req map[string]any, resp any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := hub.Dial(ctx, r.id, r.cl)
	if err != nil {
		return err
	}
	defer c.CloseWithError(0, "")
	_, _, err = hub.Request(ctx, c, req, resp)
	return err
}

func (r remoteAdmin) Invite(uses int, ttl time.Duration, admin bool) (string, string, string, error) {
	var res struct {
		wire.Reply
		Code string `json:"code"`
		FP   string `json:"fp"`
	}
	if err := r.call(map[string]any{"op": "invite", "uses": uses, "ttl_min": int(ttl.Minutes()), "admin": admin}, &res); err != nil {
		return "", "", "", err
	}
	if res.Error != "" {
		return "", "", "", errors.New(res.Error)
	}
	addr := r.cl.Hub
	if h, p, err := net.SplitHostPort(addr); err == nil && p == strconv.Itoa(config.DefaultPort) {
		addr = h
	}
	return res.Code, addr, res.FP, nil
}

func (r remoteAdmin) Devices() ([]hub.Device, error) {
	var res struct {
		wire.Reply
		Devices []hub.Device `json:"devices"`
	}
	if err := r.call(map[string]any{"op": "devices"}, &res); err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return res.Devices, nil
}

func (r remoteAdmin) Revoke(name string) error {
	var res wire.Reply
	if err := r.call(map[string]any{"op": "revoke", "target": name}, &res); err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

// cmdPairLocal (re)pareia o usuário atual ao ponto fixo deste computador.
// Útil se o usuário local foi revogado por engano.
func cmdPairLocal() error {
	if err := requireAdmin(); err != nil {
		return err
	}
	m, ok := localHubActive()
	if !ok {
		return errors.New("este computador não é o ponto fixo")
	}
	cl, err := pairLocalUser(m.Port)
	if err != nil {
		return err
	}
	fmt.Printf("Seu usuário foi pareado ao ponto fixo local como %q.\n", cl.Name)
	return nil
}
