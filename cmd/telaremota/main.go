// telaremota: acesso remoto direto, sem servidores de terceiros.
//
// Um computador alcançável (IP público/UPnP ou IPv6) roda o "ponto fixo".
// Os demais se pareiam a ele e mantêm uma conexão de saída (funciona atrás
// de CGNAT). Qualquer dispositivo pareado pode então controlar qualquer outro.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"golang.org/x/sys/windows"

	"telaremota/internal/access"
	"telaremota/internal/config"
	"telaremota/internal/e2e"
	"telaremota/internal/host"
	"telaremota/internal/hub"
	"telaremota/internal/identity"
	"telaremota/internal/screen"
	"telaremota/internal/service"
	"telaremota/internal/tailnet"
	"telaremota/internal/viewer"
	"telaremota/internal/winui"
	"telaremota/internal/wire"
)

var version = "dev"

const usage = `TelaRemota %s — acesso remoto direto, sem servidores de terceiros

  telaremota painel                      painel gráfico (ou duplo clique no executável)

PONTO FIXO (computador com IP público / UPnP / IPv6 — ex.: empresa)
  telaremota servico instalar -ponto-fixo -agente -nome empresa
  telaremota convite                     gera código de pareamento (15 min)
  telaremota dispositivos                lista dispositivos pareados
  telaremota parear-local                (re)pareia seu usuário ao ponto fixo deste computador
  telaremota remover <nome>              revoga um dispositivo

DEMAIS COMPUTADORES (ex.: casa, atrás de CGNAT)
  telaremota parear <endereço> <código> -nome casa
  telaremota servico instalar -agente
  telaremota senha [-remover]            senha de acesso pedida a quem conectar (acesso autônomo)

USO
  telaremota lista                       computadores online
  telaremota conectar <nome>             abre a tela remota
        Ctrl+Alt+Enter = tela cheia · clique direito no título = Ctrl+Alt+Del, qualidade

OUTROS
  telaremota ponto-fixo [-agente]        roda o ponto fixo em primeiro plano (teste)
  telaremota agente                      roda o agente em primeiro plano (teste)
  telaremota servico remover

REDE TAILSCALE EMBUTIDA (sem instalar o Tailscale)
  telaremota servico instalar ... -tailnet -tailnet-chave tskey-auth-...
        o serviço entra na sua rede Tailscale; use o endereço 100.x.y.z no convite
  telaremota servico instalar -tailnet -tailnet-chave tskey-auth-...
        sem -agente: só controla outros computadores (ninguém controla este)

Porta padrão: UDP %d. Comandos de máquina (servico, parear, convite) exigem administrador.
`

func main() {
	screen.SetDPIAware()
	windows.SetConsoleOutputCP(65001)
	log.SetFlags(log.Ldate | log.Ltime)

	if service.IsService() {
		runService()
		return
	}
	// Processos do usuário alcançam a rede Tailscale embutida pelo relay do serviço.
	hub.PacketDialer = tailnet.RelayDialer(config.MachineDir())
	if len(os.Args) < 2 {
		if ownsConsole() { // aberto com duplo clique: painel gráfico
			freeConsole()
			if err := cmdPanel(); err != nil {
				winui.MessageBox(err.Error(), "TelaRemota", winui.MBIconErr)
			}
			return
		}
		fmt.Printf(usage, version, config.DefaultPort)
		os.Exit(1)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "painel":
		if ownsConsole() {
			freeConsole()
		}
		err = cmdPanel()
	case "ponto-fixo":
		err = cmdHub(args)
	case "agente":
		err = cmdAgent(args)
	case "convite":
		err = cmdInvite(args)
	case "parear-local":
		err = cmdPairLocal()
	case "senha":
		err = cmdPassword(args)
	case "parear":
		err = cmdPair(args)
	case "conectar":
		err = cmdConnect(args)
	case "lista":
		err = cmdList()
	case "dispositivos":
		err = cmdDevices()
	case "remover":
		err = cmdRemove(args)
	case "servico":
		err = cmdService(args)
	case "auxiliar":
		err = cmdHelper(args)
	case "ajuda", "-h", "--help", "help":
		fmt.Printf(usage, version, config.DefaultPort)
	default:
		fmt.Printf(usage, version, config.DefaultPort)
		err = fmt.Errorf("comando desconhecido: %s", cmd)
	}
	if err != nil {
		if os.Getenv("TELAREMOTA_GUI") == "1" { // aberto pelo painel: não há console visível
			winui.MessageBox(err.Error(), "TelaRemota", winui.MBIconErr)
		}
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}

// parse aceita flags antes ou depois dos argumentos posicionais.
func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func requireAdmin() error {
	if config.Overridden() || service.IsElevated() {
		return nil
	}
	return errors.New("este comando precisa de um terminal aberto como administrador")
}

func machinePath(f string) string { return filepath.Join(config.MachineDir(), f) }
func userPath(f string) string    { return filepath.Join(config.UserDir(), f) }

func interruptCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// ---------------------------------------------------------------- nós (ponto fixo + agente)

func runNodes(ctx context.Context, m config.Machine, inService bool) error {
	id, err := identity.LoadOrCreate(config.MachineDir())
	if err != nil {
		return err
	}
	if m.Name == "" {
		m.Name = config.Hostname()
	}
	if m.Port == 0 {
		m.Port = config.DefaultPort
	}
	errc := make(chan error, 1)
	opt := hub.Options{Dir: config.MachineDir(), Port: m.Port, UPnP: m.UPnP, Name: m.Name}
	if m.Tailnet {
		node, err := tailnet.Start(ctx, config.MachineDir(), m.Name)
		if err != nil {
			return err
		}
		hub.PacketDialer = node.PacketDialer
		opt.ExtraListener = func(ctx context.Context) (net.PacketConn, error) { return node.ListenPacket(ctx, m.Port) }
		go func() {
			if err := node.ServeRelay(ctx); err != nil {
				log.Printf("%v", err)
			}
		}()
	}
	if m.Hub {
		h := hub.New(opt, id)
		go func() { errc <- h.Run(ctx) }()
	}
	if m.Agent {
		var cl config.Client
		if m.Hub {
			cl = config.Client{Hub: fmt.Sprintf("127.0.0.1:%d", m.Port), HubFP: id.FP, Name: m.Name}
		} else if err := config.Load(machinePath("client.json"), &cl); err != nil {
			if !m.Tailnet {
				return errors.New("este computador não está pareado a um ponto fixo: rode 'telaremota parear <endereço> <código>' como administrador")
			}
			// Com a rede embutida o serviço sobe antes do pareamento (o
			// pareamento passa por ele). Reinicie o serviço depois de parear.
			log.Printf("agente aguardando pareamento; só a rede Tailscale embutida está ativa")
			cl.Hub = ""
		}
		if cl.Hub != "" {
			go hub.RunAgent(ctx, id, &cl, sessionHandler(inService, id))
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

func sessionHandler(inService bool, id *identity.Identity) hub.SessionHandler {
	var busy atomic.Bool
	lim := &access.Limiter{}
	return func(s *quic.Stream, br *bufio.Reader, from, fromFP string) {
		defer s.Close()
		defer s.CancelRead(0)
		if !busy.CompareAndSwap(false, true) {
			wire.WriteJSON(s, wire.Reply{Error: "já existe uma sessão ativa neste computador"})
			return
		}
		defer busy.Store(false)

		wire.WriteJSON(s, wire.Reply{OK: true})
		tc, err := e2e.Server(br, s, id, fromFP)
		if err != nil {
			log.Printf("sessão de %q recusada: %v", from, err)
			return
		}
		defer tc.Close()
		tr := bufio.NewReaderSize(tc, 64<<10)
		if err := access.Serve(tc, tr, config.MachineDir(), lim); err != nil {
			log.Printf("sessão de %q recusada na senha de acesso: %v", from, err)
			return
		}
		if !inService {
			host.Serve(tr, tc, from)
			return
		}
		hp, err := service.StartHelper("auxiliar", "-de", from)
		if err != nil {
			log.Printf("auxiliar: %v", err)
			return
		}
		log.Printf("sessão de %q iniciada, cifrada de ponta a ponta (auxiliar pid %d)", from, hp.Cmd.Process.Pid)
		go func() { io.Copy(hp.Stdin, tr); hp.Stdin.Close() }()
		io.Copy(tc, hp.Stdout)
		done := make(chan error, 1)
		go func() { done <- hp.Cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			hp.Cmd.Process.Kill()
			<-done
		}
		log.Printf("sessão de %q encerrada", from)
	}
}

func cmdHub(args []string) error {
	fs := flag.NewFlagSet("ponto-fixo", flag.ExitOnError)
	port := fs.Int("porta", config.DefaultPort, "porta UDP")
	name := fs.String("nome", config.Hostname(), "nome deste computador")
	noUPnP := fs.Bool("sem-upnp", false, "não abrir a porta no roteador")
	agent := fs.Bool("agente", false, "permitir também que este computador seja acessado")
	ts := fs.Bool("tailnet", false, "entrar na rede Tailscale pelo nó embutido")
	parse(fs, args)
	ctx, cancel := interruptCtx()
	defer cancel()
	return runNodes(ctx, config.Machine{Hub: true, Port: *port, UPnP: !*noUPnP, Name: config.CleanName(*name), Agent: *agent, Tailnet: *ts}, false)
}

func cmdAgent(args []string) error {
	fs := flag.NewFlagSet("agente", flag.ExitOnError)
	ts := fs.Bool("tailnet", false, "entrar na rede Tailscale pelo nó embutido")
	name := fs.String("nome", config.Hostname(), "nome deste computador na rede Tailscale")
	parse(fs, args)
	ctx, cancel := interruptCtx()
	defer cancel()
	return runNodes(ctx, config.Machine{Agent: true, Tailnet: *ts, Name: config.CleanName(*name)}, false)
}

// ---------------------------------------------------------------- serviço

func runService() {
	path := machinePath("telaremota.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		os.Rename(path, path+".1")
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(f)
	}
	var m config.Machine
	if err := config.Load(machinePath("machine.json"), &m); err != nil {
		log.Printf("configuração ausente: %v", err)
		return
	}
	err := service.Run(func(ctx context.Context) {
		log.Printf("serviço iniciado (%s)", version)
		if err := runNodes(ctx, m, true); err != nil {
			log.Printf("erro: %v", err)
		}
		log.Printf("serviço parado")
	})
	if err != nil {
		log.Printf("serviço: %v", err)
	}
}

func cmdService(args []string) error {
	if len(args) == 0 {
		return errors.New("use: telaremota servico instalar|remover")
	}
	switch args[0] {
	case "executar":
		runService()
		return nil
	case "remover":
		if err := requireAdmin(); err != nil {
			return err
		}
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Serviço removido. (Dados e pareamentos continuam em", config.MachineDir()+")")
		return nil
	case "instalar":
	default:
		return fmt.Errorf("subcomando desconhecido: %s", args[0])
	}
	if err := requireAdmin(); err != nil {
		return err
	}
	fs := flag.NewFlagSet("servico instalar", flag.ExitOnError)
	isHub := fs.Bool("ponto-fixo", false, "atuar como ponto fixo")
	agent := fs.Bool("agente", false, "permitir que este computador seja acessado")
	port := fs.Int("porta", config.DefaultPort, "porta UDP do ponto fixo")
	name := fs.String("nome", config.Hostname(), "nome deste computador (ponto fixo)")
	noUPnP := fs.Bool("sem-upnp", false, "não abrir a porta no roteador")
	ts := fs.Bool("tailnet", false, "entrar na rede Tailscale pelo nó embutido (sem instalar o Tailscale)")
	tsKey := fs.String("tailnet-chave", "", "chave de autenticação Tailscale (tskey-auth-…), só no primeiro uso")
	parse(fs, args[1:])
	m := config.Machine{Hub: *isHub, Port: *port, UPnP: !*noUPnP, Name: *name, Agent: *agent, Tailnet: *ts || *tsKey != ""}
	if err := tailnet.SaveAuthKey(config.MachineDir(), *tsKey); err != nil {
		return err
	}
	exe, err := installService(m)
	if err != nil {
		return err
	}
	if m.Hub && !config.Exists(userPath("client.json")) {
		if cl, err := pairLocalUser(m.Port); err == nil {
			fmt.Printf("Seu usuário foi pareado ao ponto fixo local como %q.\n", cl.Name)
		} else {
			fmt.Println("Aviso: não foi possível parear seu usuário ao ponto fixo local:", err)
		}
	}
	fmt.Printf("Serviço TelaRemota instalado e iniciado (%s).\n", exe)
	if m.Hub {
		fmt.Printf("  Ponto fixo %q na porta UDP %d. Gere convites com: telaremota convite\n", m.Name, m.Port)
	}
	if m.Agent {
		fmt.Println("  Este computador pode ser acessado a qualquer momento (inclusive tela de bloqueio).")
	}
	fmt.Println("  Log:", machinePath("telaremota.log"))
	return nil
}

func cmdHelper(args []string) error {
	fs := flag.NewFlagSet("auxiliar", flag.ExitOnError)
	from := fs.String("de", "?", "")
	parse(fs, args)
	if f, err := os.OpenFile(machinePath("auxiliar.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(f)
	} else {
		log.SetOutput(io.Discard)
	}
	if err := host.AttachWinSta0(); err != nil {
		log.Printf("WinSta0: %v", err)
	}
	return host.Serve(os.Stdin, os.Stdout, *from)
}

// ---------------------------------------------------------------- pareamento

func cmdInvite(args []string) error {
	fs := flag.NewFlagSet("convite", flag.ExitOnError)
	uses := fs.Int("usos", 4, "quantos dispositivos podem usar o código")
	ttl := fs.Duration("validade", 15*time.Minute, "validade do código")
	adm := fs.Bool("admin", false, "usuários pareados com este código podem gerar convites e revogar dispositivos")
	parse(fs, args)
	a, err := adminFor()
	if err != nil {
		return err
	}
	if _, local := a.(localAdmin); local {
		if err := requireAdmin(); err != nil {
			return err
		}
	}
	code, addr, fp, err := a.Invite(*uses, *ttl, *adm)
	if err != nil {
		return err
	}
	fmt.Printf("\n  Código de convite:  %s   (%s)\n", code, a.Where())
	fmt.Printf("  Válido por %s, para até %d pareamento(s).", ttl.Round(time.Minute), *uses)
	if *adm {
		fmt.Print(" Convite de ADMINISTRADOR.")
	}
	fmt.Printf("\n\n  Convite completo (cole no campo Endereço do painel do outro computador):\n\n      %s %s\n\n", addr, code)
	fmt.Printf("  ou: telaremota parear %s %s -nome <nome-do-computador>\n", addr, code)
	fmt.Printf("  Impressão digital do ponto fixo: %s\n\n", identity.Short(fp))
	return nil
}

func hostPort(h string, port int) string {
	if port == config.DefaultPort {
		return h // "parear" completa a porta padrão (IPv6 pode ir sem colchetes)
	}
	return net.JoinHostPort(h, strconv.Itoa(port))
}

func cmdPair(args []string) error {
	fs := flag.NewFlagSet("parear", flag.ExitOnError)
	name := fs.String("nome", config.Hostname(), "nome deste computador")
	pos := parse(fs, args)
	if len(pos) == 1 { // linha inteira do convite entre aspas: "endereço código"
		pos = strings.Fields(pos[0])
	}
	if len(pos) != 2 {
		return errors.New("use: telaremota parear <endereço> <código> [-nome casa]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	withMachine := config.Overridden() || service.IsElevated()
	if !withMachine {
		fmt.Println("(terminal sem administrador: pareando só o seu usuário, para controlar outros computadores)")
	}
	res, err := pairAll(ctx, pos[0], pos[1], *name, withMachine)
	if res != nil && res.MachineMsg != "" {
		fmt.Println(res.MachineMsg)
		if res.Machine != nil {
			fmt.Println("  Para permitir acesso a ele a qualquer momento: telaremota servico instalar -agente")
		}
	}
	if err != nil {
		return err
	}
	fmt.Printf("Usuário pareado como %q. Veja os computadores com: telaremota lista\n", res.User.Name)
	return nil
}

// ---------------------------------------------------------------- uso

func userClient() (*identity.Identity, *config.Client, error) {
	var cl config.Client
	if err := config.Load(userPath("client.json"), &cl); err != nil {
		return nil, nil, errors.New("seu usuário não está pareado: rode 'telaremota parear <endereço> <código>'")
	}
	id, err := identity.LoadOrCreate(config.UserDir())
	return id, &cl, err
}

func cmdList() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	l, err := listOnline(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Ponto fixo %q (%s) — você é %q\n", l.Hub, l.HubAddr, l.You)
	if len(l.Agents) == 0 {
		fmt.Println("  nenhum computador online")
	}
	for _, a := range l.Agents {
		fmt.Println("  •", a)
	}
	return nil
}

func cmdConnect(args []string) error {
	fs := flag.NewFlagSet("conectar", flag.ExitOnError)
	diag := fs.Duration("diag", 0, "testa a conexão sem abrir janela por este tempo (ex.: 5s)")
	senha := fs.String("senha", "", "senha de acesso do computador remoto (se omitida, é perguntada)")
	pos := parse(fs, args)
	if len(pos) != 1 {
		return errors.New("use: telaremota conectar <nome>   (veja os nomes com: telaremota lista)")
	}
	target := pos[0]
	id, cl, err := userClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	tc, closeFn, err := hub.OpenSession(ctx, id, cl, target, userPath("known_peers.json"))
	cancel()
	if err != nil {
		return err
	}
	defer closeFn()

	// Senha de acesso do computador remoto (se ele tiver uma).
	tr := bufio.NewReaderSize(tc, 256<<10)
	remember := false
	pw, err := access.Client(tc, tr, askPassword(target, *senha, os.Getenv("TELAREMOTA_GUI") == "1", &remember))
	if err != nil {
		if errors.Is(err, access.ErrCanceled) {
			if *senha != "" {
				return errors.New("senha de acesso incorreta")
			}
			return nil
		}
		return err
	}
	if pw != "" && remember {
		rememberPassword(target, pw)
	}

	if *diag > 0 {
		return diagnose(tc, tr, *diag)
	}
	fmt.Printf("Conectado a %q via %s (cifrado de ponta a ponta). Feche a janela para encerrar.\n", target, cl.Hub)
	return viewer.Run(target, tr, tc)
}

func diagnose(s *tls.Conn, br *bufio.Reader, d time.Duration) error {
	w := wire.NewWriter(s)
	w.Send(wire.Quality, []byte{60})
	w.Send(wire.Refresh)
	start := time.Now()
	deadline := start.Add(d)
	s.SetReadDeadline(deadline)
	var frames, rects, bytes int
	var size string
	var first time.Duration
	for {
		t, p, err := wire.Read(br)
		if err != nil {
			break
		}
		bytes += len(p) + 5
		switch t {
		case wire.Size:
			size = fmt.Sprintf("%d×%d", int(p[0])<<8|int(p[1]), int(p[2])<<8|int(p[3]))
		case wire.Rect:
			rects++
		case wire.FrameEnd:
			if frames == 0 {
				first = time.Since(start)
			}
			frames++
			w.Send(wire.Ack, p)
		}
	}
	secs := time.Since(start).Seconds()
	fmt.Printf("tela %s · %d quadros (%d regiões) em %.1fs · primeiro quadro em %s · %.2f Mb/s\n",
		size, frames, rects, secs, first.Round(time.Millisecond), float64(bytes)*8/1e6/secs)
	if frames == 0 {
		return errors.New("nenhum quadro recebido")
	}
	return nil
}

// ---------------------------------------------------------------- administração

func cmdDevices() error {
	a, err := adminFor()
	if err != nil {
		return err
	}
	list, err := a.Devices()
	if err != nil {
		return err
	}
	fmt.Println("Dispositivos do", a.Where())
	if len(list) == 0 {
		fmt.Println("  nenhum. Gere um convite com: telaremota convite")
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

func cmdRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("use: telaremota remover <nome>")
	}
	a, err := adminFor()
	if err != nil {
		return err
	}
	if err := a.Revoke(args[0]); err != nil {
		return err
	}
	fmt.Printf("%q removido do %s. Conexões ativas dele caem em até 10 segundos.\n", args[0], a.Where())
	return nil
}
