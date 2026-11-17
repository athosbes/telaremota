// Package hub implementa o "ponto fixo": o computador alcançável pela
// internet que aceita conexões QUIC de dispositivos pareados e liga
// visualizadores a agentes (inclusive agentes que discaram de dentro de
// um CGNAT — conexão reversa).
package hub

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"telaremota/internal/auth"
	"telaremota/internal/config"
	"telaremota/internal/identity"
	"telaremota/internal/wire"
)

// QUICConfig é compartilhada por ponto fixo e clientes.
func QUICConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:             30 * time.Second,
		KeepAlivePeriod:            10 * time.Second, // mantém o mapeamento do NAT/CGNAT aberto
		HandshakeIdleTimeout:       10 * time.Second,
		MaxIncomingStreams:         64,
		MaxStreamReceiveWindow:     16 << 20,
		MaxConnectionReceiveWindow: 32 << 20,
	}
}

type Options struct {
	Dir  string // diretório de dados da máquina
	Port int
	UPnP bool
	Name string // nome deste computador

	// ExtraListener, se definido, devolve um socket adicional onde o ponto
	// fixo também atende (ex.: rede Tailscale embutida). Pode demorar: é
	// chamado em segundo plano.
	ExtraListener func(ctx context.Context) (net.PacketConn, error)
}

// PacketDialer, se definido, pode fornecer o socket para alcançar addr
// (ex.: rede Tailscale embutida). ok=false: usa a rede normal.
var PacketDialer func(ctx context.Context, addr string) (pc net.PacketConn, raddr net.Addr, ok bool, err error)

// tunnelQUICConfig: dentro de túneis (MTU 1280 do Tailscale) usamos pacotes pequenos.
func tunnelQUICConfig() *quic.Config {
	c := QUICConfig()
	c.InitialPacketSize = 1200
	c.DisablePathMTUDiscovery = true
	return c
}

// dialQUIC conecta a addr pela rede normal ou pelo PacketDialer.
func dialQUIC(ctx context.Context, addr string, tc *tls.Config) (*quic.Conn, error) {
	if PacketDialer != nil {
		pc, raddr, ok, err := PacketDialer(ctx, addr)
		if err != nil {
			return nil, err
		}
		if ok {
			tr := &quic.Transport{Conn: pc}
			c, err := tr.Dial(ctx, raddr, tc, tunnelQUICConfig())
			if err != nil {
				tr.Close()
				pc.Close()
				return nil, err
			}
			go func() { <-c.Context().Done(); tr.Close(); pc.Close() }()
			return c, nil
		}
	}
	return quic.DialAddr(ctx, addr, tc, QUICConfig())
}

type Hub struct {
	opt  Options
	id   *identity.Identity
	devs *devices

	mu     sync.Mutex
	agents map[string]*quic.Conn
	conns  map[*quic.Conn]string // conexão -> impressão digital
	pairMu sync.Mutex
}

func New(opt Options, id *identity.Identity) *Hub {
	return &Hub{
		opt:    opt,
		id:     id,
		devs:   &devices{path: DevicesPath(opt.Dir)},
		agents: map[string]*quic.Conn{},
		conns:  map[*quic.Conn]string{},
	}
}

// lookup devolve o dispositivo autorizado. O próprio computador é sempre aceito.
func (h *Hub) lookup(fp string) (Device, bool) {
	if fp == h.id.FP {
		return Device{FP: fp, Name: h.opt.Name, Kind: "maquina"}, true
	}
	return h.devs.find(fp)
}

func (h *Hub) verify(fp string) error {
	if _, ok := h.lookup(fp); ok {
		return nil
	}
	if _, err := loadInvite(h.opt.Dir); err == nil {
		return nil // só poderá parear
	}
	return errors.New("dispositivo não autorizado")
}

// Run escuta até ctx terminar.
func (h *Hub) Run(ctx context.Context) error {
	ln, err := quic.ListenAddr(fmt.Sprintf(":%d", h.opt.Port), h.id.ServerTLS(h.verify), QUICConfig())
	if err != nil {
		return fmt.Errorf("não foi possível abrir a porta UDP %d: %w", h.opt.Port, err)
	}
	defer ln.Close()

	st := Status{Name: h.opt.Name, Port: h.opt.Port, FP: h.id.FP, Updated: time.Now()}
	st.IPv6, st.LAN, st.VPN = LocalAddrs()
	if h.opt.UPnP {
		if ip, err := mapPort(ctx, h.opt.Port, log.Printf); err != nil {
			log.Printf("UPnP: %v — libere a porta UDP %d manualmente no roteador", err, h.opt.Port)
		} else {
			st.UPnP, st.PublicIP = true, ip
			log.Printf("UPnP: porta UDP %d aberta no roteador (IP público %s)", h.opt.Port, ip)
		}
	}
	config.Save(StatusPath(h.opt.Dir), st)
	log.Printf("ponto fixo %q ouvindo na porta UDP %d — impressão digital %s", h.opt.Name, h.opt.Port, identity.Short(h.id.FP))

	go h.revokeLoop(ctx)
	go func() { <-ctx.Done(); ln.Close() }()
	if h.opt.ExtraListener != nil {
		go h.serveExtra(ctx)
	}
	return h.accept(ctx, ln)
}

func (h *Hub) accept(ctx context.Context, ln *quic.Listener) error {
	for {
		c, err := ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go h.handleConn(c)
	}
}

// serveExtra atende também no socket adicional (rede Tailscale embutida).
func (h *Hub) serveExtra(ctx context.Context) {
	pc, err := h.opt.ExtraListener(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("ponto fixo: socket adicional indisponível: %v", err)
		}
		return
	}
	tr := &quic.Transport{Conn: pc}
	defer pc.Close()
	defer tr.Close()
	ln, err := tr.Listen(h.id.ServerTLS(h.verify), tunnelQUICConfig())
	if err != nil {
		log.Printf("ponto fixo: %v", err)
		return
	}
	go func() { <-ctx.Done(); ln.Close() }()
	log.Printf("ponto fixo também ouvindo em %s (rede Tailscale embutida)", pc.LocalAddr())
	if err := h.accept(ctx, ln); err != nil {
		log.Printf("ponto fixo (rede Tailscale embutida): %v", err)
	}
}

// revokeLoop derruba conexões de dispositivos removidos.
func (h *Hub) revokeLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.mu.Lock()
		var drop []*quic.Conn
		for c, fp := range h.conns {
			if _, ok := h.lookup(fp); !ok {
				drop = append(drop, c)
			}
		}
		h.mu.Unlock()
		for _, c := range drop {
			log.Printf("acesso revogado: desconectando %s", c.RemoteAddr())
			c.CloseWithError(1, "acesso revogado")
		}
	}
}

func (h *Hub) handleConn(c *quic.Conn) {
	fp := identity.PeerFP(c.ConnectionState().TLS)
	dev, ok := h.lookup(fp)
	if !ok {
		h.handlePairing(c, fp)
		return
	}
	h.mu.Lock()
	h.conns[c] = fp
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.conns, c)
		for name, ac := range h.agents {
			if ac == c {
				delete(h.agents, name)
				log.Printf("agente %q offline", name)
			}
		}
		h.mu.Unlock()
	}()
	for {
		s, err := c.AcceptStream(c.Context())
		if err != nil {
			return
		}
		go h.handleStream(c, dev, s)
	}
}

type request struct {
	Op     string `json:"op"`
	Target string `json:"target,omitempty"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Uses   int    `json:"uses,omitempty"`
	TTL    int    `json:"ttl_min,omitempty"`
	Admin  bool   `json:"admin,omitempty"`
}

func (h *Hub) handleStream(c *quic.Conn, dev Device, s *quic.Stream) {
	br := bufio.NewReader(s)
	s.SetReadDeadline(time.Now().Add(15 * time.Second))
	var req request
	if err := wire.ReadJSON(br, &req); err != nil {
		s.CancelRead(0)
		s.Close()
		return
	}
	s.SetReadDeadline(time.Time{})

	switch req.Op {
	case "register":
		h.mu.Lock()
		old := h.agents[dev.Name]
		h.agents[dev.Name] = c
		h.mu.Unlock()
		if old != nil && old != c {
			old.CloseWithError(2, "substituído por nova conexão")
		}
		log.Printf("agente %q online (%s)", dev.Name, c.RemoteAddr())
		wire.WriteJSON(s, map[string]any{"ok": true, "name": dev.Name, "hub": h.opt.Name})
		s.Close()

	case "list":
		h.mu.Lock()
		names := make([]string, 0, len(h.agents))
		for n := range h.agents {
			names = append(names, n)
		}
		h.mu.Unlock()
		sort.Strings(names)
		wire.WriteJSON(s, map[string]any{"ok": true, "agents": names, "you": dev.Name, "hub": h.opt.Name})
		s.Close()

	case "connect":
		h.mu.Lock()
		ac := h.agents[req.Target]
		targetFP := h.conns[ac]
		h.mu.Unlock()
		if ac == nil {
			wire.WriteJSON(s, wire.Reply{Error: fmt.Sprintf("computador %q não está online", req.Target)})
			s.Close()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		as, err := ac.OpenStreamSync(ctx)
		cancel()
		if err != nil {
			wire.WriteJSON(s, wire.Reply{Error: "falha ao abrir canal com o computador remoto"})
			s.Close()
			return
		}
		// As impressões digitais permitem que visualizador e agente façam um
		// aperto de mão TLS próprio, de ponta a ponta, por dentro deste canal.
		wire.WriteJSON(as, map[string]any{"op": "session", "from": dev.Name, "from_fp": dev.FP})
		wire.WriteJSON(s, map[string]any{"ok": true, "target_fp": targetFP})
		log.Printf("sessão: %q -> %q", dev.Name, req.Target)
		relay(s, br, as)
		log.Printf("sessão encerrada: %q -> %q", dev.Name, req.Target)

	case "pair":
		wire.WriteJSON(s, wire.Reply{Error: "este dispositivo já está pareado"})
		s.Close()

	case "invite", "devices", "revoke":
		defer s.Close()
		if !dev.Admin {
			wire.WriteJSON(s, wire.Reply{Error: "seu usuário não é administrador deste ponto fixo (pareie-o com um convite de administrador)"})
			return
		}
		h.admin(s, dev, req)

	default:
		wire.WriteJSON(s, wire.Reply{Error: "operação desconhecida"})
		s.Close()
	}
}

// admin atende operações de administração remota.
func (h *Hub) admin(s *quic.Stream, dev Device, req request) {
	switch req.Op {
	case "invite":
		uses, ttl := max(1, min(req.Uses, 10)), time.Duration(max(1, min(req.TTL, 60)))*time.Minute
		code, err := NewInvite(h.opt.Dir, uses, ttl, req.Admin)
		if err != nil {
			wire.WriteJSON(s, wire.Reply{Error: err.Error()})
			return
		}
		log.Printf("convite gerado remotamente por %q (admin=%v)", dev.Name, req.Admin)
		wire.WriteJSON(s, map[string]any{"ok": true, "code": code, "uses": uses, "ttl_min": int(ttl.Minutes()), "hub": h.opt.Name, "fp": h.id.FP})
	case "devices":
		list, err := ListDevices(h.opt.Dir)
		if err != nil {
			wire.WriteJSON(s, wire.Reply{Error: err.Error()})
			return
		}
		wire.WriteJSON(s, map[string]any{"ok": true, "devices": list})
	case "revoke":
		if req.Target == dev.Name {
			wire.WriteJSON(s, wire.Reply{Error: "você não pode revogar o próprio usuário por aqui"})
			return
		}
		if err := RemoveDevice(h.opt.Dir, req.Target); err != nil {
			wire.WriteJSON(s, wire.Reply{Error: err.Error()})
			return
		}
		log.Printf("dispositivo %q revogado remotamente por %q", req.Target, dev.Name)
		wire.WriteJSON(s, wire.Reply{OK: true})
	}
}

// relay copia bytes nos dois sentidos até um lado terminar.
func relay(a *quic.Stream, ar io.Reader, b *quic.Stream) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(b, ar); b.Close(); done <- struct{}{} }()
	go func() { io.Copy(a, b); a.Close(); done <- struct{}{} }()
	<-done
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	a.CancelRead(0)
	b.CancelRead(0)
}

// handlePairing: conexão de dispositivo desconhecido; só aceitamos "pair".
func (h *Hub) handlePairing(c *quic.Conn, fpClient string) {
	defer c.CloseWithError(0, "")
	ctx, cancel := context.WithTimeout(c.Context(), 20*time.Second)
	s, err := c.AcceptStream(ctx)
	cancel()
	if err != nil {
		return
	}
	s.SetDeadline(time.Now().Add(2 * time.Minute))
	br := bufio.NewReader(s)
	// Antes de fechar, espera o cliente ler a resposta (ele fecha o stream).
	defer func() {
		s.Close()
		s.SetReadDeadline(time.Now().Add(5 * time.Second))
		br.ReadByte()
	}()
	var req request
	if wire.ReadJSON(br, &req) != nil || req.Op != "pair" {
		wire.WriteJSON(s, wire.Reply{Error: "dispositivo não pareado"})
		return
	}

	h.pairMu.Lock()
	inv, err := loadInvite(h.opt.Dir)
	h.pairMu.Unlock()
	if err != nil {
		wire.WriteJSON(s, wire.Reply{Error: err.Error()})
		return
	}
	salt, _ := base64.StdEncoding.DecodeString(inv.Salt)
	key, _ := base64.StdEncoding.DecodeString(inv.Key)
	nonce := auth.RandomB64(24)
	wire.WriteJSON(s, map[string]any{"salt": base64.StdEncoding.EncodeToString(salt), "nonce": nonce, "iter": auth.Iterations})

	var pr struct {
		Proof string `json:"proof"`
	}
	if wire.ReadJSON(br, &pr) != nil {
		return
	}
	got, _ := base64.StdEncoding.DecodeString(pr.Proof)

	h.pairMu.Lock()
	defer h.pairMu.Unlock()
	inv, err = loadInvite(h.opt.Dir) // relê: outro pareamento pode ter consumido
	if err != nil {
		wire.WriteJSON(s, wire.Reply{Error: err.Error()})
		return
	}
	if !hmac.Equal(got, auth.Proof(key, "pair-client", nonce, fpClient, h.id.FP)) {
		inv.Failures++
		saveInvite(h.opt.Dir, inv)
		log.Printf("pareamento recusado (código incorreto) de %s", c.RemoteAddr())
		time.Sleep(time.Second)
		wire.WriteJSON(s, wire.Reply{Error: "código de convite incorreto"})
		return
	}
	kind := "usuario"
	if req.Kind == "maquina" {
		kind = "maquina"
	}
	dev, err := h.devs.add(Device{FP: fpClient, Name: config.CleanName(req.Name), Kind: kind, Added: time.Now(),
		Admin: inv.Admin && kind == "usuario"}, h.opt.Name)
	if err != nil {
		wire.WriteJSON(s, wire.Reply{Error: "erro ao salvar dispositivo"})
		return
	}
	inv.Uses--
	saveInvite(h.opt.Dir, inv)
	log.Printf("dispositivo pareado: %q (%s, admin=%v) de %s", dev.Name, kind, dev.Admin, c.RemoteAddr())
	wire.WriteJSON(s, map[string]any{
		"ok":       true,
		"admin":    dev.Admin,
		"name":     dev.Name,
		"hub_name": h.opt.Name,
		"proof":    base64.StdEncoding.EncodeToString(auth.Proof(key, "pair-hub", nonce, fpClient, h.id.FP)),
	})
}

// Addr formata host:porta, aceitando IPv6 sem colchetes e porta omitida.
func Addr(s string) string {
	if _, _, err := net.SplitHostPort(s); err == nil {
		return s
	}
	return net.JoinHostPort(strings.Trim(s, "[]"), fmt.Sprint(config.DefaultPort))
}
