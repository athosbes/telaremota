// Agente TelaRemota: roda no computador que será acessado.
package main

import (
	"bufio"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sys/windows"
	"golang.org/x/term"

	"telaremota/internal/auth"
	"telaremota/internal/proto"
	"telaremota/internal/screen"
)

// Pode ser sobrescrito no build: -ldflags "-X main.defaultServer=https://remoto.exemplo.com"
var (
	defaultServer = "http://localhost:8080"
	version       = "dev"
)

type credential struct {
	salt    []byte
	key     []byte
	display string // senha temporária a exibir; vazio quando é senha fixa
}

type agent struct {
	cfg     *Config
	cfgPath string
	confirm bool

	wmu sync.Mutex
	ws  *websocket.Conn

	mu        sync.Mutex
	id        string
	sess      *session
	cred      credential
	failures  int
	lockUntil time.Time
}

func main() {
	screen.SetDPIAware()
	windows.SetConsoleOutputCP(65001)
	log.SetFlags(log.Ltime)

	var (
		serverFlag = flag.String("server", "", "endereço do servidor (ex: https://remoto.exemplo.com)")
		cfgFlag    = flag.String("config", "", "arquivo de configuração (padrão: %APPDATA%\\TelaRemota\\config.json)")
		setPw      = flag.Bool("definir-senha", false, "define uma senha fixa (acesso não assistido) e sai")
		rmPw       = flag.Bool("remover-senha", false, "remove a senha fixa (volta à senha temporária) e sai")
		confirm    = flag.Bool("confirmar", false, "pede confirmação na tela antes de liberar cada sessão")
	)
	flag.Parse()

	path := *cfgFlag
	if path == "" {
		var err error
		if path, err = defaultConfigPath(); err != nil {
			log.Fatal(err)
		}
	}
	cfg, err := loadConfig(path)
	if err != nil {
		log.Fatalf("erro ao ler configuração: %v", err)
	}
	if *serverFlag != "" {
		cfg.Server = *serverFlag
	}
	if cfg.Server == "" {
		cfg.Server = defaultServer
	}
	if cfg.Key == "" {
		cfg.Key = auth.RandomB64(32)
	}

	switch {
	case *setPw:
		if err := setFixedPassword(cfg); err != nil {
			log.Fatal(err)
		}
		must(saveConfig(path, cfg))
		fmt.Println("Senha fixa definida.")
		return
	case *rmPw:
		cfg.PassSalt, cfg.PassKey = "", ""
		must(saveConfig(path, cfg))
		fmt.Println("Senha fixa removida.")
		return
	}
	must(saveConfig(path, cfg))

	a := &agent{cfg: cfg, cfgPath: path, confirm: *confirm}
	a.cred = a.newCredential()
	fmt.Printf("TelaRemota agente %s — servidor %s\n", version, cfg.Server)
	a.run()
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func setFixedPassword(cfg *Config) error {
	read := func(prompt string) (string, error) {
		fmt.Print(prompt)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			return string(b), err
		}
		s, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return s, err
	}
	p1, err := read("Nova senha fixa (mín. 10 caracteres): ")
	if err != nil {
		return err
	}
	p2, err := read("Confirme a senha: ")
	if err != nil {
		return err
	}
	n := auth.Normalize(p1)
	if n != auth.Normalize(p2) {
		return errors.New("as senhas não conferem")
	}
	if len(n) < 10 {
		return errors.New("senha curta demais (mínimo 10 caracteres, sem contar espaços e hífens)")
	}
	salt := auth.Random(16)
	cfg.PassSalt = base64.StdEncoding.EncodeToString(salt)
	cfg.PassKey = base64.StdEncoding.EncodeToString(auth.DeriveKey(n, salt))
	return nil
}

func (a *agent) newCredential() credential {
	if a.cfg.PassKey != "" {
		salt, err1 := base64.StdEncoding.DecodeString(a.cfg.PassSalt)
		key, err2 := base64.StdEncoding.DecodeString(a.cfg.PassKey)
		if err1 == nil && err2 == nil {
			return credential{salt: salt, key: key}
		}
		log.Printf("senha fixa inválida na configuração; usando senha temporária")
	}
	pw := auth.GeneratePassword()
	salt := auth.Random(16)
	return credential{salt: salt, key: auth.DeriveKey(auth.Normalize(pw), salt), display: pw}
}

func (a *agent) credential() credential {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cred
}

func (a *agent) banner() {
	a.mu.Lock()
	id, pw := a.id, a.cred.display
	a.mu.Unlock()
	if pw == "" {
		pw = "(senha fixa configurada)"
	}
	fmt.Println()
	fmt.Println("  ┌─────────────────── TelaRemota ───────────────────")
	fmt.Printf("  │  ID:     %s\n", formatID(id))
	fmt.Printf("  │  Senha:  %s\n", pw)
	fmt.Printf("  │  Link:   %s\n", viewerLink(a.cfg.Server, id))
	fmt.Println("  │")
	fmt.Println("  │  Passe o ID e a senha apenas para quem você confia.")
	fmt.Println("  │  Feche esta janela para encerrar o acesso.")
	fmt.Println("  └──────────────────────────────────────────────────")
	fmt.Println()
}

// lockRemaining informa quanto tempo falta de bloqueio por senhas erradas.
func (a *agent) lockRemaining() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Until(a.lockUntil)
}

func (a *agent) authFailed() {
	a.mu.Lock()
	a.failures++
	n := a.failures
	if n >= 3 { // 5s, 10s, 20s ... até ~21min
		a.lockUntil = time.Now().Add(time.Duration(5<<min(n-3, 8)) * time.Second)
	}
	regen := n >= 5 && a.cred.display != ""
	if regen {
		a.cred = a.newCredential()
	}
	a.mu.Unlock()
	log.Printf("senha incorreta (%d tentativa(s) seguidas)", n)
	if regen {
		log.Printf("muitas tentativas incorretas: uma nova senha foi gerada")
		a.banner()
	}
}

func (a *agent) authOK() {
	a.mu.Lock()
	a.failures = 0
	a.mu.Unlock()
}

func (a *agent) send(m proto.Msg) {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	if a.ws == nil {
		return
	}
	a.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := a.ws.WriteJSON(m); err != nil {
		a.ws.Close()
	}
}

func (a *agent) session(sid string) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess != nil && a.sess.sid == sid {
		return a.sess
	}
	return nil
}

func (a *agent) run() {
	backoff := time.Second
	for {
		start := time.Now()
		err := a.connectOnce()
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		log.Printf("desconectado do servidor: %v — tentando novamente em %s", err, backoff)
		time.Sleep(backoff)
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (a *agent) connectOnce() error {
	u, err := wsURL(a.cfg.Server)
	if err != nil {
		return err
	}
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, _, err := d.Dial(u, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	const readTimeout = 90 * time.Second
	conn.SetReadLimit(1 << 20)
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	conn.SetPingHandler(func(s string) error {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(s), time.Now().Add(5*time.Second))
	})

	a.wmu.Lock()
	a.ws = conn
	a.wmu.Unlock()
	defer func() {
		a.wmu.Lock()
		a.ws = nil
		a.wmu.Unlock()
	}()

	a.send(proto.Msg{T: "register", ID: a.cfg.ID, Key: a.cfg.Key})
	for {
		var m proto.Msg
		if err := conn.ReadJSON(&m); err != nil {
			return err
		}
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		switch m.T {
		case "registered":
			a.mu.Lock()
			changed := a.id != m.ID
			a.id = m.ID
			a.mu.Unlock()
			if a.cfg.ID != m.ID {
				a.cfg.ID = m.ID
				if err := saveConfig(a.cfgPath, a.cfg); err != nil {
					log.Printf("aviso: não foi possível salvar o ID: %v", err)
				}
			}
			if changed {
				a.banner()
			} else {
				log.Printf("reconectado ao servidor")
			}
		case "incoming":
			a.incoming(m)
		case "signal":
			if s := a.session(m.SID); s != nil {
				s.signal(m.Data)
			}
		case "bye":
			if s := a.session(m.SID); s != nil {
				s.close("encerrada pelo visualizador")
			}
		case "error":
			log.Printf("servidor: %s", m.Msg)
		}
	}
}

func (a *agent) incoming(m proto.Msg) {
	a.mu.Lock()
	if a.sess != nil {
		a.mu.Unlock()
		log.Printf("conexão de %s recusada: já existe uma sessão ativa", m.Msg)
		a.send(proto.Msg{T: "bye", SID: m.SID})
		return
	}
	s, err := newSession(a, a.cred, m.SID, m.Msg, m.ICE) // a.mu já está travado aqui
	if err != nil {
		a.mu.Unlock()
		log.Printf("erro ao criar sessão: %v", err)
		a.send(proto.Msg{T: "bye", SID: m.SID})
		return
	}
	a.sess = s
	a.mu.Unlock()
	log.Printf("pedido de conexão de %s", m.Msg)
}
