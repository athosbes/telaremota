package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"telaremota/internal/proto"
)

const (
	pingEvery   = 25 * time.Second
	readTimeout = 70 * time.Second
	writeWait   = 10 * time.Second
)

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}

type client struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
	ip   string

	// protegidos por hub.mu
	role string // "agent" | "viewer"
	id   string
	sids map[string]struct{}
}

func (c *client) out(m proto.Msg) {
	b, _ := json.Marshal(m)
	select {
	case c.send <- b:
	case <-c.done:
	default: // cliente lento demais: desconecta
		c.close()
	}
}

func (c *client) close() { c.once.Do(func() { close(c.done); c.conn.Close() }) }

func (c *client) writePump() {
	t := time.NewTicker(pingEvery)
	defer func() { t.Stop(); c.close() }()
	for {
		select {
		case b := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if c.conn.WriteMessage(websocket.TextMessage, b) != nil {
				return
			}
		case <-t.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if c.conn.WriteMessage(websocket.PingMessage, nil) != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

type session struct{ agent, viewer *client }

func (s *session) peer(c *client) *client {
	switch c {
	case s.agent:
		return s.viewer
	case s.viewer:
		return s.agent
	}
	return nil
}

type hub struct {
	mu         sync.Mutex
	agents     map[string]*client
	sessions   map[string]*session
	store      *store
	ice        func(sid string) []proto.ICEServer
	limiter    *limiter
	trustProxy bool
}

func newHub(st *store, ice func(string) []proto.ICEServer) *hub {
	return &hub{
		agents:   map[string]*client{},
		sessions: map[string]*session{},
		store:    st,
		ice:      ice,
		limiter:  newLimiter(10, time.Minute), // 10 tentativas de conexão por minuto por IP
	}
}

func (h *hub) clientIP(r *http.Request) string {
	if h.trustProxy {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func (h *hub) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, send: make(chan []byte, 128), done: make(chan struct{}), ip: h.clientIP(r), sids: map[string]struct{}{}}
	go c.writePump()
	defer func() { h.drop(c); c.close() }()

	conn.SetReadLimit(64 << 10)
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(readTimeout)); return nil })
	for {
		var m proto.Msg
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		h.handle(c, m)
	}
}

func (h *hub) handle(c *client, m proto.Msg) {
	switch m.T {
	case "register":
		h.mu.Lock()
		if c.role != "" {
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()
		id, err := h.store.claim(m.ID, m.Key)
		if err != nil {
			c.out(proto.Msg{T: "error", Msg: err.Error()})
			c.close()
			return
		}
		h.mu.Lock()
		if old := h.agents[id]; old != nil && old != c {
			old.close() // reconexão do mesmo agente: derruba a conexão antiga
		}
		c.role, c.id = "agent", id
		h.agents[id] = c
		h.mu.Unlock()
		log.Printf("agente %s online (%s)", id, c.ip)
		c.out(proto.Msg{T: "registered", ID: id})

	case "connect":
		if !h.limiter.allow(c.ip) {
			c.out(proto.Msg{T: "error", Msg: "Muitas tentativas. Aguarde um minuto."})
			return
		}
		id := digits(m.ID)
		h.mu.Lock()
		if c.role != "" {
			h.mu.Unlock()
			return
		}
		a := h.agents[id]
		if a == nil {
			h.mu.Unlock()
			c.out(proto.Msg{T: "error", Msg: "ID não encontrado ou computador offline."})
			return
		}
		sid := randomHex(16)
		c.role = "viewer"
		h.sessions[sid] = &session{agent: a, viewer: c}
		a.sids[sid] = struct{}{}
		c.sids[sid] = struct{}{}
		h.mu.Unlock()
		log.Printf("sessão %s: %s -> agente %s", sid[:8], c.ip, id)
		ice := h.ice(sid)
		a.out(proto.Msg{T: "incoming", SID: sid, ICE: ice, Msg: c.ip})
		c.out(proto.Msg{T: "session", SID: sid, ICE: ice})

	case "signal", "bye":
		h.mu.Lock()
		s := h.sessions[m.SID]
		var peer *client
		if s != nil {
			peer = s.peer(c)
		}
		if peer != nil && m.T == "bye" {
			h.endLocked(m.SID, s)
		}
		h.mu.Unlock()
		if peer != nil {
			peer.out(proto.Msg{T: m.T, SID: m.SID, Data: m.Data})
		}
	}
}

func (h *hub) endLocked(sid string, s *session) {
	delete(h.sessions, sid)
	delete(s.agent.sids, sid)
	delete(s.viewer.sids, sid)
}

func (h *hub) drop(c *client) {
	type note struct {
		to  *client
		sid string
	}
	var notes []note
	h.mu.Lock()
	if c.role == "agent" && h.agents[c.id] == c {
		delete(h.agents, c.id)
		log.Printf("agente %s offline", c.id)
	}
	for sid := range c.sids {
		if s := h.sessions[sid]; s != nil {
			notes = append(notes, note{s.peer(c), sid})
			h.endLocked(sid, s)
		}
	}
	h.mu.Unlock()
	for _, n := range notes {
		n.to.out(proto.Msg{T: "bye", SID: n.sid})
	}
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// limiter é um token bucket simples por IP.
type limiter struct {
	mu     sync.Mutex
	rate   float64 // tokens por segundo
	burst  float64
	bucket map[string]*bucket
	last   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(n int, per time.Duration) *limiter {
	return &limiter{rate: float64(n) / per.Seconds(), burst: float64(n), bucket: map[string]*bucket{}}
}

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.last) > 10*time.Minute { // limpeza periódica
		for k, b := range l.bucket {
			if now.Sub(b.at) > 10*time.Minute {
				delete(l.bucket, k)
			}
		}
		l.last = now
	}
	b := l.bucket[ip]
	if b == nil {
		b = &bucket{tokens: l.burst, at: now}
		l.bucket[ip] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
