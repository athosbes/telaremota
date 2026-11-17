// telaremota-hub: ponto fixo do TelaRemota para servidores (Linux, mas
// compila em qualquer sistema). Só encaminha conexões: as sessões são
// cifradas de ponta a ponta entre os computadores.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"telaremota/internal/config"
	"telaremota/internal/hub"
	"telaremota/internal/identity"
)

var version = "dev"

const usage = `telaremota-hub %s — ponto fixo do TelaRemota

  telaremota-hub instalar [-nome nuvem] [-porta 47070]   instala o serviço (systemd) e libera o firewall
  telaremota-hub convite [-admin] [-usos 4] [-validade 15m]
  telaremota-hub dispositivos
  telaremota-hub remover <nome>
  telaremota-hub desinstalar
  telaremota-hub executar [-nome nuvem] [-porta 47070]   roda em primeiro plano

Dados em %s. Log: journalctl -u telaremota -f
`

type hubConfig struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

func cfgPath() string { return filepath.Join(config.MachineDir(), "hub.json") }

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		fmt.Printf(usage, version, config.MachineDir())
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "executar":
		err = cmdRun(os.Args[2:])
	case "instalar":
		err = cmdInstall(os.Args[2:])
	case "desinstalar":
		err = cmdUninstall()
	case "convite":
		err = cmdInvite(os.Args[2:])
	case "dispositivos":
		err = cmdDevices()
	case "remover":
		if len(os.Args) != 3 {
			err = errors.New("use: telaremota-hub remover <nome>")
		} else if err = hub.RemoveDevice(config.MachineDir(), os.Args[2]); err == nil {
			fmt.Printf("%q removido; conexões dele caem em até 10 segundos.\n", os.Args[2])
		}
	default:
		fmt.Printf(usage, version, config.MachineDir())
		err = fmt.Errorf("comando desconhecido: %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

func loadCfg(fs *flag.FlagSet, args []string) hubConfig {
	c := hubConfig{Name: "nuvem", Port: config.DefaultPort}
	config.Load(cfgPath(), &c)
	name := fs.String("nome", c.Name, "nome do ponto fixo")
	port := fs.Int("porta", c.Port, "porta UDP")
	fs.Parse(args)
	return hubConfig{Name: config.CleanName(*name), Port: *port}
}

func cmdRun(args []string) error {
	c := loadCfg(flag.NewFlagSet("executar", flag.ExitOnError), args)
	id, err := identity.LoadOrCreate(config.MachineDir())
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Printf("telaremota-hub %s", version)
	return hub.New(hub.Options{Dir: config.MachineDir(), Port: c.Port, UPnP: false, Name: c.Name}, id).Run(ctx)
}

const unit = `[Unit]
Description=TelaRemota - ponto fixo
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s executar
Restart=always
RestartSec=3
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=%s

[Install]
WantedBy=multi-user.target
`

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %v %s", name, args, err, out)
	}
	return nil
}

func cmdInstall(args []string) error {
	if runtime.GOOS != "linux" {
		return errors.New("instalar só é suportado em Linux com systemd; use 'executar' em primeiro plano")
	}
	if os.Geteuid() != 0 {
		return errors.New("execute com sudo")
	}
	c := loadCfg(flag.NewFlagSet("instalar", flag.ExitOnError), args)
	dir := config.MachineDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := config.Save(cfgPath(), c); err != nil {
		return err
	}
	id, err := identity.LoadOrCreate(dir)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if err := os.WriteFile("/etc/systemd/system/telaremota.service", []byte(fmt.Sprintf(unit, exe, dir)), 0o644); err != nil {
		return err
	}
	port := strconv.Itoa(c.Port)
	for _, ipt := range []string{"iptables", "ip6tables"} {
		if _, err := exec.LookPath(ipt); err != nil {
			continue
		}
		if exec.Command(ipt, "-C", "INPUT", "-p", "udp", "--dport", port, "-j", "ACCEPT").Run() != nil {
			if err := run(ipt, "-I", "INPUT", "1", "-p", "udp", "--dport", port, "-j", "ACCEPT"); err != nil {
				fmt.Println("aviso:", err)
			}
		}
	}
	if _, err := exec.LookPath("netfilter-persistent"); err == nil {
		if err := run("netfilter-persistent", "save"); err != nil {
			fmt.Println("aviso: não foi possível salvar as regras do firewall:", err)
		}
	} else {
		fmt.Println("aviso: netfilter-persistent não encontrado; a regra do firewall pode sumir ao reiniciar (instale: sudo apt install iptables-persistent)")
	}
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "enable", "--now", "telaremota"); err != nil {
		return err
	}
	run("systemctl", "restart", "telaremota")
	time.Sleep(time.Second)
	fmt.Printf("Ponto fixo %q instalado e em execução na porta UDP %d.\n", c.Name, c.Port)
	fmt.Printf("Impressão digital: %s\n", identity.Short(id.FP))
	fmt.Println("Lembre de liberar UDP", c.Port, "também na rede da nuvem (Security List da Oracle).")
	fmt.Println("Próximo passo: sudo telaremota-hub convite -admin")
	return nil
}

func cmdUninstall() error {
	if os.Geteuid() != 0 {
		return errors.New("execute com sudo")
	}
	run("systemctl", "disable", "--now", "telaremota")
	os.Remove("/etc/systemd/system/telaremota.service")
	run("systemctl", "daemon-reload")
	fmt.Println("Serviço removido. Dados mantidos em", config.MachineDir())
	return nil
}

func cmdInvite(args []string) error {
	fs := flag.NewFlagSet("convite", flag.ExitOnError)
	uses := fs.Int("usos", 4, "quantos dispositivos podem usar o código")
	ttl := fs.Duration("validade", 15*time.Minute, "validade")
	adm := fs.Bool("admin", false, "usuários pareados com este código poderão gerar convites e revogar pelo painel")
	fs.Parse(args)
	code, err := hub.NewInvite(config.MachineDir(), *uses, *ttl, *adm)
	if err != nil {
		return fmt.Errorf("%w (use sudo)", err)
	}
	id, _ := identity.LoadOrCreate(config.MachineDir())
	kind := ""
	if *adm {
		kind = " de ADMINISTRADOR"
	}
	fmt.Printf("\n  Código de convite%s:  %s\n", kind, code)
	fmt.Printf("  Válido por %s, para até %d pareamento(s).\n\n", ttl.Round(time.Minute), *uses)
	fmt.Println("  No Windows: painel → \"Este computador\" → Endereço = IP público deste servidor, Código = acima.")
	if id != nil {
		fmt.Printf("  Impressão digital do ponto fixo: %s\n\n", identity.Short(id.FP))
	}
	return nil
}

func cmdDevices() error {
	list, err := hub.ListDevices(config.MachineDir())
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("Nenhum dispositivo pareado.")
		return nil
	}
	fmt.Printf("%-24s %-8s %-6s %-17s %s\n", "NOME", "TIPO", "ADMIN", "PAREADO EM", "IMPRESSÃO DIGITAL")
	for _, d := range list {
		adm := ""
		if d.Admin {
			adm = "sim"
		}
		fmt.Printf("%-24s %-8s %-6s %-17s %s\n", d.Name, d.Kind, adm, d.Added.Format("02/01/2006 15:04"), identity.Short(d.FP))
	}
	return nil
}
