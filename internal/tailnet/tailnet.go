// Package tailnet embute um nó Tailscale (tsnet) no próprio TelaRemota:
// sem instalar o Tailscale, sem placa de rede virtual e sem driver. A rede
// roda dentro do processo do serviço.
//
// Só o serviço (conta SYSTEM) entra na rede Tailscale. Os processos do
// usuário (painel, visualizador) a alcançam por um relay UDP em 127.0.0.1,
// assim cada computador aparece uma única vez no painel do Tailscale.
package tailnet

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"tailscale.com/tsnet"

	"telaremota/internal/config"
)

// RelayPort é a porta UDP local (somente 127.0.0.1) do relay para processos do usuário.
const RelayPort = config.DefaultPort + 1

// relayPort permite, em testes, dois nós na mesma máquina (TELAREMOTA_RELAY_PORT).
func relayPort() int {
	if p, err := strconv.Atoi(os.Getenv("TELAREMOTA_RELAY_PORT")); err == nil && p > 0 {
		return p
	}
	return RelayPort
}

// Arquivos no diretório da máquina.
func StatusPath(dir string) string  { return filepath.Join(dir, "tailnet.json") }
func AuthKeyPath(dir string) string { return filepath.Join(dir, "tailnet-authkey") }

// Status é gravado pelo serviço para o painel mostrar.
type Status struct {
	State   string    `json:"state"`              // NeedsLogin, Starting, Running…
	AuthURL string    `json:"auth_url,omitempty"` // link de login, quando não há chave
	IP      string    `json:"ip,omitempty"`       // 100.x.y.z
	DNSName string    `json:"dns_name,omitempty"` // empresa.tailnet-xxxx.ts.net
	Error   string    `json:"error,omitempty"`
	Updated time.Time `json:"updated"`
}

// LoadStatus lê o estado gravado pelo serviço (vazio se nunca rodou).
func LoadStatus(dir string) Status {
	var st Status
	config.Load(StatusPath(dir), &st)
	return st
}

// Running informa se o nó embutido está conectado (status recente).
func (s Status) Running() bool {
	return s.State == "Running" && s.IP != "" && time.Since(s.Updated) < 30*time.Second
}

// Node é o nó Tailscale embutido.
type Node struct {
	srv *tsnet.Server
	dir string

	mu sync.Mutex
	ip netip.Addr
	up chan struct{}
}

// Start inicia o nó com o nome hostname. A chave de autenticação, se houver,
// é lida de tailnet-authkey e apagada depois do primeiro login bem-sucedido.
func Start(ctx context.Context, dir, hostname string) (*Node, error) {
	key, _ := os.ReadFile(AuthKeyPath(dir))
	srv := &tsnet.Server{
		Dir:      filepath.Join(dir, "tailnet"),
		Hostname: hostname,
		AuthKey:  strings.TrimSpace(string(key)),
		// Testes: o nó some do painel do Tailscale ao desligar.
		Ephemeral: os.Getenv("TELAREMOTA_TAILNET_EFEMERO") == "1",
		Logf:      func(string, ...any) {}, // registro interno é muito verboso
		UserLogf:  dedupLog(),
	}
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	n := &Node{srv: srv, dir: dir, up: make(chan struct{})}
	go n.watch(ctx)
	go func() { <-ctx.Done(); srv.Close() }()
	return n, nil
}

// dedupLog registra mensagens do tsnet sem repetir a anterior (o aviso de
// login se repete a cada poucos segundos).
func dedupLog() func(string, ...any) {
	var mu sync.Mutex
	var last string
	return func(f string, a ...any) {
		m := fmt.Sprintf(f, a...)
		mu.Lock()
		defer mu.Unlock()
		if m == last {
			return
		}
		last = m
		log.Printf("tailnet: %s", m)
	}
}

// watch grava o estado periodicamente e sinaliza quando o nó sobe.
func (n *Node) watch(ctx context.Context) {
	lc, err := n.srv.LocalClient()
	if err != nil {
		config.Save(StatusPath(n.dir), Status{State: "Erro", Error: err.Error(), Updated: time.Now()})
		return
	}
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	upOnce := sync.Once{}
	for {
		st := Status{Updated: time.Now()}
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		s, err := lc.Status(qctx)
		cancel()
		if err != nil {
			st.State, st.Error = "Erro", err.Error()
		} else {
			st.State, st.AuthURL = s.BackendState, s.AuthURL
			for _, ip := range s.TailscaleIPs {
				if ip.Is4() {
					st.IP = ip.String()
					n.mu.Lock()
					n.ip = ip
					n.mu.Unlock()
				}
			}
			if s.Self != nil {
				st.DNSName = strings.TrimSuffix(s.Self.DNSName, ".")
			}
			if st.State == "Running" && st.IP != "" {
				upOnce.Do(func() {
					log.Printf("tailnet: conectado como %s (%s)", st.DNSName, st.IP)
					os.Remove(AuthKeyPath(n.dir)) // a chave não é mais necessária
					close(n.up)
				})
			}
		}
		config.Save(StatusPath(n.dir), st)
		select {
		case <-ctx.Done():
			os.Remove(StatusPath(n.dir))
			return
		case <-t.C:
		}
	}
}

// WaitUp espera o nó estar conectado e devolve o IP Tailscale.
func (n *Node) WaitUp(ctx context.Context) (netip.Addr, error) {
	select {
	case <-n.up:
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.ip, nil
	case <-ctx.Done():
		return netip.Addr{}, ctx.Err()
	}
}

// ListenPacket abre a porta UDP port no IP Tailscale (espera o nó subir).
func (n *Node) ListenPacket(ctx context.Context, port int) (net.PacketConn, error) {
	ip, err := n.WaitUp(ctx)
	if err != nil {
		return nil, err
	}
	return n.srv.ListenPacket("udp", netip.AddrPortFrom(ip, uint16(port)).String())
}

// ephemeral abre uma porta UDP qualquer no IP Tailscale, para conexões de saída.
func (n *Node) ephemeral(ctx context.Context) (net.PacketConn, error) {
	ip, err := n.WaitUp(ctx)
	if err != nil {
		return nil, err
	}
	var last error
	for range 8 {
		pc, err := n.srv.ListenPacket("udp", netip.AddrPortFrom(ip, uint16(20000+rand.IntN(40000))).String())
		if err == nil {
			return pc, nil
		}
		last = err
	}
	return nil, last
}

// IsTailnetIP informa se ip pertence à faixa da rede Tailscale (100.64.0.0/10).
func IsTailnetIP(ip netip.Addr) bool {
	return netip.MustParsePrefix("100.64.0.0/10").Contains(ip.Unmap())
}

// tailnetAddr resolve "100.x.y.z:porta" (só IPs Tailscale; nomes ficam com a rede normal).
func tailnetAddr(addr string) (netip.AddrPort, bool) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil || !IsTailnetIP(ap.Addr()) {
		return netip.AddrPort{}, false
	}
	return ap, true
}

// PacketDialer serve como hub.PacketDialer no processo do serviço.
func (n *Node) PacketDialer(ctx context.Context, addr string) (net.PacketConn, net.Addr, bool, error) {
	ap, ok := tailnetAddr(addr)
	if !ok {
		return nil, nil, false, nil
	}
	pc, err := n.ephemeral(ctx)
	if err != nil {
		return nil, nil, true, fmt.Errorf("rede Tailscale embutida indisponível: %w", err)
	}
	return pc, net.UDPAddrFromAddrPort(ap), true, nil
}

// ---------------------------------------------------------------- relay local

// Protocolo do relay (UDP em 127.0.0.1:RelayPort): cada datagrama leva
// 4 bytes de IPv4 + 2 de porta do destino (ou da origem, na volta) e o conteúdo.
const hdr = 6

// ServeRelay encaminha datagramas de processos locais para a rede Tailscale.
// Só aceita destinos na faixa Tailscale.
func (n *Node) ServeRelay(ctx context.Context) error {
	lc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: relayPort()})
	if err != nil {
		return fmt.Errorf("relay tailnet: %w", err)
	}
	go func() { <-ctx.Done(); lc.Close() }()

	type flow struct {
		pc   net.PacketConn
		last time.Time
	}
	var mu sync.Mutex
	flows := map[netip.AddrPort]*flow{}

	go func() { // fecha fluxos ociosos
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			mu.Lock()
			for k, f := range flows {
				if time.Since(f.last) > 2*time.Minute {
					f.pc.Close()
					delete(flows, k)
				}
			}
			mu.Unlock()
		}
	}()

	buf := make([]byte, 64<<10)
	for {
		nr, src, err := lc.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if nr <= hdr {
			continue
		}
		dst := netip.AddrPortFrom(netip.AddrFrom4([4]byte(buf[:4])), uint16(buf[4])<<8|uint16(buf[5]))
		if !IsTailnetIP(dst.Addr()) {
			continue
		}
		mu.Lock()
		f := flows[src]
		if f == nil {
			pc, err := n.ephemeral(ctx)
			if err != nil {
				mu.Unlock()
				continue
			}
			f = &flow{pc: pc}
			flows[src] = f
			go func(src netip.AddrPort, pc net.PacketConn) { // volta: rede Tailscale -> processo local
				b := make([]byte, 64<<10)
				for {
					m, from, err := pc.ReadFrom(b[hdr:])
					if err != nil {
						return
					}
					ua, ok := from.(*net.UDPAddr)
					if !ok {
						continue
					}
					ap := ua.AddrPort()
					a4 := ap.Addr().Unmap().As4()
					copy(b, a4[:])
					b[4], b[5] = byte(ap.Port()>>8), byte(ap.Port())
					lc.WriteToUDPAddrPort(b[:hdr+m], src)
				}
			}(src, pc)
		}
		f.last = time.Now()
		mu.Unlock()
		f.pc.WriteTo(buf[hdr:nr], net.UDPAddrFromAddrPort(dst))
	}
}

// relayConn é o lado do cliente: um net.PacketConn que fala com o relay local.
type relayConn struct {
	*net.UDPConn
	relay *net.UDPAddr
}

func (r *relayConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, errors.New("relay tailnet: endereço inválido")
	}
	ap := ua.AddrPort()
	if !ap.Addr().Unmap().Is4() {
		return 0, errors.New("relay tailnet: só IPv4")
	}
	b := make([]byte, hdr+len(p))
	a4 := ap.Addr().Unmap().As4()
	copy(b, a4[:])
	b[4], b[5] = byte(ap.Port()>>8), byte(ap.Port())
	copy(b[hdr:], p)
	if _, err := r.UDPConn.WriteToUDP(b, r.relay); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *relayConn) ReadFrom(p []byte) (int, net.Addr, error) {
	b := make([]byte, hdr+len(p))
	for {
		n, _, err := r.UDPConn.ReadFromUDP(b)
		if err != nil {
			return 0, nil, err
		}
		if n <= hdr {
			continue
		}
		ap := netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[:4])), uint16(b[4])<<8|uint16(b[5]))
		return copy(p, b[hdr:n]), net.UDPAddrFromAddrPort(ap), nil
	}
}

// RelayDialer serve como hub.PacketDialer nos processos do usuário: usa o
// relay do serviço quando o destino é um IP Tailscale e o nó embutido está ativo.
func RelayDialer(machineDir string) func(context.Context, string) (net.PacketConn, net.Addr, bool, error) {
	return func(ctx context.Context, addr string) (net.PacketConn, net.Addr, bool, error) {
		ap, ok := tailnetAddr(addr)
		if !ok || !LoadStatus(machineDir).Running() {
			return nil, nil, false, nil // segue pela rede normal (ex.: Tailscale instalado)
		}
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return nil, nil, true, err
		}
		return &relayConn{UDPConn: c, relay: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: relayPort()}},
			net.UDPAddrFromAddrPort(ap), true, nil
	}
}

// SaveAuthKey grava a chave para o serviço usar no próximo início.
func SaveAuthKey(dir, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	if !strings.HasPrefix(key, "tskey-") {
		return errors.New("chave Tailscale inválida (deve começar com tskey-)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(AuthKeyPath(dir), []byte(key), 0o600)
}
