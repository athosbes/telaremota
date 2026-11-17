package main

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"golang.org/x/sys/windows"

	"telaremota/internal/auth"
	"telaremota/internal/clipboard"
	"telaremota/internal/input"
	"telaremota/internal/proto"
)

type session struct {
	a    *agent
	sid  string
	peer string // IP do visualizador (informativo)
	pc   *webrtc.PeerConnection
	cred credential

	mu        sync.Mutex
	remoteSet bool
	pending   []webrtc.ICECandidateInit
	ctl       *webrtc.DataChannel
	video     *webrtc.DataChannel
	authTried bool

	videoOpen chan struct{}
	videoOnce sync.Once
	nonce     string
	authed    atomic.Bool
	done      chan struct{}
	once      sync.Once

	inflight  atomic.Int32
	ackCh     chan struct{}
	quality   atomic.Int32
	forceFull atomic.Bool
	sw, sh    atomic.Int32
	inp       *input.Injector
	clipSkip  atomic.Uint32
}

func newSession(a *agent, cred credential, sid, peer string, ice []proto.ICEServer) (*session, error) {
	var servers []webrtc.ICEServer
	for _, s := range ice {
		srv := webrtc.ICEServer{URLs: s.URLs, Username: s.Username}
		if s.Credential != "" {
			srv.Credential = s.Credential
		}
		servers = append(servers, srv)
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		return nil, err
	}
	s := &session{
		a: a, sid: sid, peer: peer, pc: pc, cred: cred,
		videoOpen: make(chan struct{}),
		nonce:     auth.RandomB64(24),
		done:      make(chan struct{}),
		ackCh:     make(chan struct{}, 1),
		inp:       input.New(),
	}
	s.quality.Store(60)

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			s.sendSignal(map[string]any{"candidate": c.ToJSON()})
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateConnected:
			log.Printf("canal P2P criptografado estabelecido com %s", s.peer)
		case webrtc.PeerConnectionStateFailed:
			go s.close("falha na conexão P2P (talvez seja necessário um servidor TURN)")
		case webrtc.PeerConnectionStateClosed:
			go s.close("conexão fechada")
		}
	})
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "ctl":
			s.mu.Lock()
			s.ctl = dc
			s.mu.Unlock()
			dc.OnOpen(func() {
				s.sendCtl(map[string]any{
					"t":     "hello",
					"salt":  base64.StdEncoding.EncodeToString(s.cred.salt),
					"nonce": s.nonce,
					"iter":  auth.Iterations,
				})
			})
			dc.OnMessage(func(msg webrtc.DataChannelMessage) { s.onCtl(msg.Data) })
			dc.OnClose(func() { go s.close("canal de controle fechado") })
		case "video":
			s.mu.Lock()
			s.video = dc
			s.mu.Unlock()
			dc.OnOpen(func() { s.videoOnce.Do(func() { close(s.videoOpen) }) })
		default:
			dc.Close()
		}
	})
	time.AfterFunc(60*time.Second, func() {
		if !s.authed.Load() {
			s.close("tempo para autenticação esgotado")
		}
	})
	return s, nil
}

func (s *session) isDone() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *session) close(reason string) {
	s.once.Do(func() {
		close(s.done)
		s.inp.ReleaseAll()
		go s.pc.Close()
		s.a.mu.Lock()
		if s.a.sess == s {
			s.a.sess = nil
		}
		s.a.mu.Unlock()
		s.a.send(proto.Msg{T: "bye", SID: s.sid})
		log.Printf("sessão encerrada: %s", reason)
	})
}

func (s *session) sendSignal(v any) {
	b, _ := json.Marshal(v)
	s.a.send(proto.Msg{T: "signal", SID: s.sid, Data: b})
}

func (s *session) sendCtl(v any) {
	s.mu.Lock()
	dc := s.ctl
	s.mu.Unlock()
	if dc == nil {
		return
	}
	b, _ := json.Marshal(v)
	dc.SendText(string(b))
}

// signal trata SDP/ICE vindos do visualizador.
func (s *session) signal(raw json.RawMessage) {
	var d struct {
		SDP       *webrtc.SessionDescription `json:"sdp"`
		Candidate *webrtc.ICECandidateInit   `json:"candidate"`
	}
	if json.Unmarshal(raw, &d) != nil || s.isDone() {
		return
	}
	switch {
	case d.SDP != nil:
		if d.SDP.Type != webrtc.SDPTypeOffer {
			return
		}
		if err := s.pc.SetRemoteDescription(*d.SDP); err != nil {
			s.close(fmt.Sprintf("oferta inválida: %v", err))
			return
		}
		ans, err := s.pc.CreateAnswer(nil)
		if err == nil {
			err = s.pc.SetLocalDescription(ans)
		}
		if err != nil {
			s.close(fmt.Sprintf("erro ao responder: %v", err))
			return
		}
		s.mu.Lock()
		s.remoteSet = true
		pend := s.pending
		s.pending = nil
		s.mu.Unlock()
		for _, c := range pend {
			s.pc.AddICECandidate(c)
		}
		s.sendSignal(map[string]any{"sdp": s.pc.LocalDescription()})
	case d.Candidate != nil:
		s.mu.Lock()
		if !s.remoteSet {
			s.pending = append(s.pending, *d.Candidate)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.pc.AddICECandidate(*d.Candidate)
	}
}

type ctlIn struct {
	T     string `json:"t"`
	Proof string `json:"proof"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	B     int    `json:"b"`
	D     bool   `json:"d"`
	DX    int    `json:"dx"`
	DY    int    `json:"dy"`
	C     string `json:"c"`
	F     uint32 `json:"f"`
	V     int    `json:"v"`
	Text  string `json:"text"`
}

func (s *session) onCtl(data []byte) {
	var m ctlIn
	if json.Unmarshal(data, &m) != nil || s.isDone() {
		return
	}
	if m.T == "auth" {
		go s.authenticate(m.Proof)
		return
	}
	if !s.authed.Load() {
		return // nada é aceito antes da autenticação
	}
	switch m.T {
	case "m":
		s.inp.MouseMove(m.X, m.Y, int(s.sw.Load()), int(s.sh.Load()))
	case "mb":
		s.inp.Button(m.B, m.D)
	case "w":
		s.inp.Wheel(m.DY, m.DX)
	case "k":
		s.inp.Key(m.C, m.D)
	case "ack":
		if s.inflight.Add(-1) < 0 {
			s.inflight.Store(0)
		}
		select {
		case s.ackCh <- struct{}{}:
		default:
		}
	case "q":
		s.quality.Store(int32(max(20, min(95, m.V))))
		s.forceFull.Store(true)
	case "refresh":
		s.forceFull.Store(true)
	case "clip":
		go func() {
			if err := clipboard.Set(m.Text); err == nil {
				s.clipSkip.Store(clipboard.Seq())
			}
		}()
	}
}

func (s *session) authenticate(proofB64 string) {
	s.mu.Lock()
	tried := s.authTried
	s.authTried = true
	s.mu.Unlock()
	if tried {
		return // uma tentativa por sessão
	}
	fail := func(msg string) {
		s.sendCtl(map[string]any{"t": "auth_fail", "msg": msg})
		time.AfterFunc(time.Second, func() { s.close("autenticação recusada") })
	}
	if wait := s.a.lockRemaining(); wait > 0 {
		fail(fmt.Sprintf("Muitas tentativas incorretas. Tente novamente em %d s.", int(wait.Seconds())+1))
		return
	}
	local, remote := s.pc.LocalDescription(), s.pc.RemoteDescription()
	if local == nil || remote == nil {
		fail("Estado de conexão inválido.")
		return
	}
	fpV, fpA := auth.Fingerprint(remote.SDP), auth.Fingerprint(local.SDP)
	got, err := base64.StdEncoding.DecodeString(proofB64)
	if fpV == "" || fpA == "" || err != nil ||
		!hmac.Equal(got, auth.Proof(s.cred.key, "viewer", s.nonce, fpV, fpA)) {
		s.a.authFailed()
		fail("Senha incorreta.")
		return
	}
	if s.a.confirm {
		log.Printf("aguardando confirmação local para %s…", s.peer)
		if !askYesNo("TelaRemota", fmt.Sprintf("Permitir que %s controle este computador?", s.peer)) {
			s.sendCtl(map[string]any{"t": "denied"})
			time.AfterFunc(time.Second, func() { s.close("recusada pelo usuário local") })
			return
		}
	}
	if s.isDone() {
		return
	}
	s.a.authOK()
	w, h := screenSize()
	s.sw.Store(int32(w))
	s.sh.Store(int32(h))
	s.authed.Store(true)
	s.sendCtl(map[string]any{
		"t":     "auth_ok",
		"proof": base64.StdEncoding.EncodeToString(auth.Proof(s.cred.key, "agent", s.nonce, fpV, fpA)),
		"w":     w,
		"h":     h,
	})
	log.Printf(">>> SESSÃO ATIVA: %s está controlando este computador", s.peer)
	go s.stream()
	go s.watchClipboard()
}

// watchClipboard envia ao visualizador o texto copiado no computador remoto.
func (s *session) watchClipboard() {
	last := clipboard.Seq()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		n := clipboard.Seq()
		if n == last {
			continue
		}
		last = n
		if n == s.clipSkip.Load() {
			continue // foi o próprio visualizador que colou
		}
		if text, err := clipboard.Get(); err == nil && text != "" {
			s.sendCtl(map[string]any{"t": "clip", "text": text})
		}
	}
}

func askYesNo(title, text string) bool {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	const flags = 0x4 | 0x20 | 0x40000 | 0x10000 // YESNO | ICONQUESTION | TOPMOST | SETFOREGROUND
	r, _ := windows.MessageBox(0, t, c, flags)
	return r == 6 // IDYES
}
