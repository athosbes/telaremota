package hub

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/quic-go/quic-go"

	"telaremota/internal/auth"
	"telaremota/internal/config"
	"telaremota/internal/e2e"
	"telaremota/internal/identity"
	"telaremota/internal/wire"
)

// Dial conecta a um ponto fixo com impressão digital fixada.
func Dial(ctx context.Context, id *identity.Identity, cl *config.Client) (*quic.Conn, error) {
	c, err := dialQUIC(ctx, cl.Hub, id.ClientTLS(cl.HubFP))
	if err != nil {
		if strings.Contains(err.Error(), "bad certificate") {
			return nil, fmt.Errorf("o ponto fixo %s recusou este dispositivo (não pareado ou removido)", cl.Hub)
		}
		return nil, fmt.Errorf("não foi possível conectar ao ponto fixo %s: %w", cl.Hub, err)
	}
	return c, nil
}

// Request abre um stream, envia req e lê a resposta.
func Request(ctx context.Context, c *quic.Conn, req any, resp any) (*quic.Stream, *bufio.Reader, error) {
	s, err := c.OpenStreamSync(ctx)
	if err != nil {
		return nil, nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		s.SetDeadline(dl)
		defer s.SetDeadline(time.Time{})
	}
	if err := wire.WriteJSON(s, req); err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(s)
	if err := wire.ReadJSON(br, resp); err != nil {
		if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, nil, errors.New("o ponto fixo não respondeu a tempo")
		}
		if strings.Contains(err.Error(), "bad certificate") {
			// No TLS 1.3 a recusa do certificado do cliente só chega após o aperto de mão.
			return nil, nil, errors.New("o ponto fixo recusou este dispositivo (não pareado, removido ou convite já usado)")
		}
		return nil, nil, err
	}
	return s, br, nil
}

// Pair pareia esta identidade com o ponto fixo em addr usando o código.
func Pair(ctx context.Context, id *identity.Identity, addr, code, name, kind string) (*config.Client, error) {
	var hubFP string
	tlsConf := id.ClientTLS("")
	c, err := dialQUIC(ctx, addr, tlsConf)
	if err != nil {
		return nil, fmt.Errorf("não foi possível conectar a %s: %w (o convite está ativo e a porta liberada?)", addr, err)
	}
	defer c.CloseWithError(0, "")
	hubFP = identity.PeerFP(c.ConnectionState().TLS)

	var hello struct {
		wire.Reply
		Salt  string `json:"salt"`
		Nonce string `json:"nonce"`
		Iter  int    `json:"iter"`
	}
	s, br, err := Request(ctx, c, map[string]any{"op": "pair", "name": name, "kind": kind}, &hello)
	if err != nil {
		return nil, fmt.Errorf("pareamento recusado: %w", err)
	}
	if hello.Error != "" {
		return nil, errors.New(hello.Error)
	}
	if hello.Iter < auth.Iterations {
		return nil, errors.New("parâmetros de segurança inválidos")
	}
	salt, _ := base64.StdEncoding.DecodeString(hello.Salt)
	key := auth.DeriveKey(auth.Normalize(code), salt)
	proof := auth.Proof(key, "pair-client", hello.Nonce, id.FP, hubFP)
	wire.WriteJSON(s, map[string]any{"proof": base64.StdEncoding.EncodeToString(proof)})

	var res struct {
		wire.Reply
		Name    string `json:"name"`
		HubName string `json:"hub_name"`
		Proof   string `json:"proof"`
	}
	if err := wire.ReadJSON(br, &res); err != nil {
		return nil, fmt.Errorf("pareamento interrompido: %w", err)
	}
	s.Close()
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	got, _ := base64.StdEncoding.DecodeString(res.Proof)
	if !hmac.Equal(got, auth.Proof(key, "pair-hub", hello.Nonce, id.FP, hubFP)) {
		return nil, errors.New("o ponto fixo não comprovou conhecer o código — possível interceptação")
	}
	return &config.Client{Hub: addr, HubFP: hubFP, HubName: res.HubName, Name: res.Name}, nil
}

// SessionHandler atende uma sessão vinda do ponto fixo. fromFP é a chave do
// visualizador, usada no aperto de mão de ponta a ponta.
type SessionHandler func(s *quic.Stream, br *bufio.Reader, from, fromFP string)

// OpenSession conecta ao computador target e devolve o canal cifrado de ponta
// a ponta. knownPath guarda as chaves já vistas de cada computador (como o
// known_hosts do SSH): se a chave mudar, a conexão é recusada.
func OpenSession(ctx context.Context, id *identity.Identity, cl *config.Client, target, knownPath string) (*tls.Conn, func(), error) {
	c, err := Dial(ctx, id, cl)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*tls.Conn, func(), error) {
		c.CloseWithError(0, "")
		return nil, nil, err
	}
	var r1 struct {
		wire.Reply
		TargetFP string `json:"target_fp"`
	}
	s, br, err := Request(ctx, c, map[string]any{"op": "connect", "target": target}, &r1)
	if err != nil {
		return fail(err)
	}
	if r1.Error != "" {
		return fail(errors.New(r1.Error))
	}
	var r2 wire.Reply
	if err := wire.ReadJSON(br, &r2); err != nil {
		return fail(fmt.Errorf("o computador remoto não respondeu: %w", err))
	}
	if r2.Error != "" {
		return fail(errors.New(r2.Error))
	}
	known := map[string]string{}
	config.Load(knownPath, &known)
	check := func(fp string) error {
		if prev, ok := known[target]; ok && prev != fp {
			return fmt.Errorf("a chave de %q mudou (era %s, agora %s). Se aquele computador foi reinstalado e pareado de novo, apague a linha dele em %s",
				target, identity.Short(prev), identity.Short(fp), knownPath)
		}
		return nil
	}
	tc, err := e2e.Client(br, s, id, r1.TargetFP, check)
	if err != nil {
		s.CancelRead(0)
		return fail(err)
	}
	if _, ok := known[target]; !ok {
		known[target] = r1.TargetFP
		config.Save(knownPath, known)
	}
	return tc, func() {
		tc.Close()
		s.CancelRead(0)
		c.CloseWithError(0, "")
	}, nil
}

// RunAgent mantém a conexão de saída com o ponto fixo (conexão reversa) e
// atende sessões. Reconecta sozinho.
func RunAgent(ctx context.Context, id *identity.Identity, cl *config.Client, handle SessionHandler) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := agentOnce(ctx, id, cl, handle)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		log.Printf("agente: %v — reconectando em %s", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func agentOnce(ctx context.Context, id *identity.Identity, cl *config.Client, handle SessionHandler) error {
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	c, err := Dial(dctx, id, cl)
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseWithError(0, "")
	var res struct {
		wire.Reply
		Name string `json:"name"`
		Hub  string `json:"hub"`
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	s, _, err := Request(rctx, c, map[string]any{"op": "register"}, &res)
	cancel()
	if err != nil {
		return err
	}
	s.Close()
	log.Printf("agente: online como %q no ponto fixo %q (%s)", res.Name, res.Hub, cl.Hub)
	for {
		s, err := c.AcceptStream(ctx)
		if err != nil {
			return err
		}
		go func() {
			br := bufio.NewReader(s)
			var hdr struct {
				Op     string `json:"op"`
				From   string `json:"from"`
				FromFP string `json:"from_fp"`
			}
			if wire.ReadJSON(br, &hdr) != nil || hdr.Op != "session" {
				s.CancelRead(0)
				s.Close()
				return
			}
			handle(s, br, hdr.From, hdr.FromFP)
		}()
	}
}
