// Package e2e cifra a sessão de ponta a ponta entre visualizador e agente.
//
// O ponto fixo apenas repassa bytes de um stream QUIC para outro. Por dentro
// desse canal, visualizador (cliente) e agente (servidor) fazem um aperto de
// mão TLS 1.3 próprio com as chaves de dispositivo Ed25519. Assim, mesmo um
// ponto fixo em servidor de nuvem não consegue ver tela, teclado ou
// área de transferência.
package e2e

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"telaremota/internal/identity"
)

const alpn = "telaremota-e2e/1"

// Stream é o necessário de um stream QUIC (ou outro transporte confiável).
type Stream interface {
	io.Writer
	io.Closer
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// conn adapta leitor + stream a net.Conn para o crypto/tls.
type conn struct {
	r io.Reader
	s Stream
}

type addr struct{}

func (addr) Network() string { return "telaremota" }
func (addr) String() string  { return "e2e" }

func (c *conn) Read(p []byte) (int, error)         { return c.r.Read(p) }
func (c *conn) Write(p []byte) (int, error)        { return c.s.Write(p) }
func (c *conn) Close() error                       { return c.s.Close() }
func (c *conn) LocalAddr() net.Addr                { return addr{} }
func (c *conn) RemoteAddr() net.Addr               { return addr{} }
func (c *conn) SetDeadline(t time.Time) error      { return c.s.SetDeadline(t) }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.s.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.s.SetWriteDeadline(t) }

func verifier(expect string, check func(fp string) error) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("sem certificado")
		}
		cert, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return err
		}
		fp := identity.Fingerprint(cert)
		if expect == "" || fp != expect {
			return fmt.Errorf("a chave do outro lado (%s) não é a informada pelo ponto fixo (%s)", identity.Short(fp), identity.Short(expect))
		}
		if check != nil {
			return check(fp)
		}
		return nil
	}
}

func handshake(tc *tls.Conn, s Stream) (*tls.Conn, error) {
	s.SetDeadline(time.Now().Add(20 * time.Second))
	err := tc.Handshake()
	s.SetDeadline(time.Time{})
	if err != nil {
		return nil, fmt.Errorf("criptografia de ponta a ponta: %w", err)
	}
	return tc, nil
}

// Client é usado pelo visualizador. expectFP vem do ponto fixo; check pode
// aplicar uma verificação extra (ex.: chave memorizada do computador).
func Client(r io.Reader, s Stream, id *identity.Identity, expectFP string, check func(fp string) error) (*tls.Conn, error) {
	cfg := &tls.Config{
		Certificates:          []tls.Certificate{id.Cert},
		MinVersion:            tls.VersionTLS13,
		NextProtos:            []string{alpn},
		InsecureSkipVerify:    true, // a verificação real é a impressão digital abaixo
		VerifyPeerCertificate: verifier(expectFP, check),
	}
	return handshake(tls.Client(&conn{r, s}, cfg), s)
}

// Server é usado pelo agente; expectFP é a chave do visualizador informada pelo ponto fixo.
func Server(r io.Reader, s Stream, id *identity.Identity, expectFP string) (*tls.Conn, error) {
	cfg := &tls.Config{
		Certificates:          []tls.Certificate{id.Cert},
		MinVersion:            tls.VersionTLS13,
		NextProtos:            []string{alpn},
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: verifier(expectFP, nil),
	}
	return handshake(tls.Server(&conn{r, s}, cfg), s)
}
