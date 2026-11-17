// Package access implementa a senha de acesso de um computador (acesso
// autônomo, no estilo AnyDesk), verificada por dentro do canal cifrado de
// ponta a ponta.
//
// O agente guarda só salt + PBKDF2(senha). Na sessão, envia {salt, nonce} e o
// visualizador responde HMAC(PBKDF2(senha), nonce ‖ EKM), onde EKM é material
// exportado da sessão TLS de ponta a ponta — a prova não serve para nenhuma
// outra sessão e a senha nunca trafega.
package access

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"telaremota/internal/auth"
	"telaremota/internal/config"
	"telaremota/internal/wire"
)

// Config é gravada em <dados da máquina>/access.json.
type Config struct {
	Salt string `json:"salt"`
	Key  string `json:"key"`
}

func Path(dir string) string { return filepath.Join(dir, "access.json") }

// Load devolve a configuração; ok=false quando não há senha definida.
func Load(dir string) (Config, bool) {
	var c Config
	if config.Load(Path(dir), &c) != nil || c.Key == "" {
		return Config{}, false
	}
	return c, true
}

// MinLen é o tamanho mínimo da senha (sem contar espaços e hífens).
const MinLen = 8

// Set define a senha (ou remove, se vazia).
func Set(dir, password string) error {
	if password == "" {
		return config.Save(Path(dir), Config{})
	}
	n := auth.Normalize(password)
	if len(n) < MinLen {
		return fmt.Errorf("a senha precisa ter pelo menos %d caracteres", MinLen)
	}
	salt := auth.Random(16)
	return config.Save(Path(dir), Config{
		Salt: base64.StdEncoding.EncodeToString(salt),
		Key:  base64.StdEncoding.EncodeToString(auth.DeriveKey(n, salt)),
	})
}

func ekm(tc *tls.Conn) ([]byte, error) {
	cs := tc.ConnectionState()
	return cs.ExportKeyingMaterial("telaremota-acesso", nil, 32)
}

func proof(key []byte, nonce string, binding []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("telaremota/acesso|" + nonce + "|"))
	m.Write(binding)
	return m.Sum(nil)
}

type challenge struct {
	Auth  string `json:"auth"` // "senha" | "nenhuma"
	Salt  string `json:"salt,omitempty"`
	Nonce string `json:"nonce,omitempty"`
	Iter  int    `json:"iter,omitempty"`
}

// Limiter bloqueia tentativas após erros seguidos (por computador).
type Limiter struct {
	mu       sync.Mutex
	failures int
	until    time.Time
}

func (l *Limiter) wait() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return time.Until(l.until)
}

func (l *Limiter) result(ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ok {
		l.failures = 0
		return
	}
	l.failures++
	if l.failures >= 3 { // 10s, 20s, 40s… até ~1h
		l.until = time.Now().Add(time.Duration(10<<min(l.failures-3, 8)) * time.Second)
	}
}

// Serve roda no agente logo após o aperto de mão de ponta a ponta.
// Permite até 3 tentativas na mesma sessão.
func Serve(tc *tls.Conn, br *bufio.Reader, dir string, lim *Limiter) error {
	cfg, ok := Load(dir)
	if !ok {
		return wire.WriteJSON(tc, challenge{Auth: "nenhuma"})
	}
	salt, _ := base64.StdEncoding.DecodeString(cfg.Salt)
	key, _ := base64.StdEncoding.DecodeString(cfg.Key)
	binding, err := ekm(tc)
	if err != nil {
		return err
	}
	for try := 0; try < 3; try++ {
		if w := lim.wait(); w > 0 {
			wire.WriteJSON(tc, wire.Reply{Error: fmt.Sprintf("muitas senhas incorretas; tente novamente em %d s", int(w.Seconds())+1)})
			return errors.New("bloqueado por tentativas incorretas")
		}
		nonce := auth.RandomB64(24)
		if err := wire.WriteJSON(tc, challenge{Auth: "senha", Salt: base64.StdEncoding.EncodeToString(salt), Nonce: nonce, Iter: auth.Iterations}); err != nil {
			return err
		}
		tc.SetReadDeadline(time.Now().Add(5 * time.Minute)) // tempo para a pessoa digitar
		var r struct {
			Proof  string `json:"proof"`
			Cancel bool   `json:"cancel"`
		}
		err := wire.ReadJSON(br, &r)
		tc.SetReadDeadline(time.Time{})
		if err != nil {
			return err
		}
		if r.Cancel {
			return errors.New("cancelado pelo visualizador")
		}
		got, _ := base64.StdEncoding.DecodeString(r.Proof)
		ok := hmac.Equal(got, proof(key, nonce, binding))
		lim.result(ok)
		if ok {
			return wire.WriteJSON(tc, wire.Reply{OK: true})
		}
		time.Sleep(time.Second)
		if err := wire.WriteJSON(tc, wire.Reply{Error: "senha incorreta"}); err != nil {
			return err
		}
	}
	return errors.New("senha incorreta")
}

// ErrCanceled indica que o usuário desistiu de digitar a senha.
var ErrCanceled = errors.New("conexão cancelada")

// Ask obtém a senha; retry=true quando a anterior foi recusada.
// Devolve ok=false se o usuário cancelar.
type Ask func(retry bool) (password string, ok bool)

// Client roda no visualizador. Devolve a senha aceita ("" se o computador não
// exige senha), para que possa ser lembrada.
func Client(tc *tls.Conn, br *bufio.Reader, ask Ask) (string, error) {
	binding, err := ekm(tc)
	if err != nil {
		return "", err
	}
	retry := false
	for {
		var ch challenge
		if err := readChallenge(br, &ch); err != nil {
			return "", err
		}
		if ch.Auth == "nenhuma" {
			return "", nil
		}
		if ch.Auth != "senha" || ch.Iter < auth.Iterations {
			return "", errors.New("resposta inválida do computador remoto")
		}
		pw, ok := ask(retry)
		if !ok {
			wire.WriteJSON(tc, map[string]any{"cancel": true})
			return "", ErrCanceled
		}
		salt, _ := base64.StdEncoding.DecodeString(ch.Salt)
		key := auth.DeriveKey(auth.Normalize(pw), salt)
		wire.WriteJSON(tc, map[string]any{"proof": base64.StdEncoding.EncodeToString(proof(key, ch.Nonce, binding))})
		var r wire.Reply
		if err := wire.ReadJSON(br, &r); err != nil {
			return "", fmt.Errorf("o computador remoto encerrou a conexão: %w", err)
		}
		if r.OK {
			return pw, nil
		}
		if r.Error != "senha incorreta" {
			return "", errors.New(r.Error)
		}
		retry = true
	}
}

// readChallenge aceita também um erro (ex.: bloqueio) no lugar do desafio.
func readChallenge(br *bufio.Reader, ch *challenge) error {
	var raw struct {
		challenge
		wire.Reply
	}
	if err := wire.ReadJSON(br, &raw); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("o computador remoto encerrou a conexão (senha incorreta várias vezes?)")
		}
		return err
	}
	if raw.Error != "" {
		return errors.New(raw.Error)
	}
	*ch = raw.challenge
	return nil
}
