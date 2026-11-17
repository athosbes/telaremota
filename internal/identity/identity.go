// Package identity gerencia a chave de dispositivo (Ed25519) e o TLS mútuo.
//
// Cada instalação tem uma chave própria. A "impressão digital" é o SHA-256
// da chave pública (SPKI) — estável mesmo se o certificado for regenerado.
// O ponto fixo só aceita impressões digitais pareadas; o cliente fixa
// (pinning) a impressão digital do ponto fixo no pareamento.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// ALPN identifica o protocolo na negociação TLS.
const ALPN = "telaremota/1"

type Identity struct {
	Cert tls.Certificate
	FP   string
}

// LoadOrCreate lê dir/identity.pem ou cria uma nova chave.
func LoadOrCreate(dir string) (*Identity, error) {
	path := filepath.Join(dir, "identity.pem")
	var key ed25519.PrivateKey
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, errors.New("identity.pem inválido")
		}
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, err
		}
		var ok bool
		if key, ok = k.(ed25519.PrivateKey); !ok {
			return nil, errors.New("identity.pem: chave não é Ed25519")
		}
	case errors.Is(err, os.ErrNotExist):
		_, key, _ = ed25519.GenerateKey(rand.Reader)
		der, _ := x509.MarshalPKCS8PrivateKey(key)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "telaremota"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	return &Identity{
		Cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		FP:   Fingerprint(leaf),
	}, nil
}

// Fingerprint = hex(SHA-256(SubjectPublicKeyInfo)).
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// Short formata a impressão digital para conferência visual.
func Short(fp string) string {
	if len(fp) < 16 {
		return fp
	}
	return fmt.Sprintf("%s-%s-%s-%s", fp[0:4], fp[4:8], fp[8:12], fp[12:16])
}

func peerFP(raw [][]byte) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("sem certificado")
	}
	c, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return "", err
	}
	return Fingerprint(c), nil
}

// PeerFP extrai a impressão digital do outro lado de uma conexão estabelecida.
func PeerFP(cs tls.ConnectionState) string {
	if len(cs.PeerCertificates) == 0 {
		return ""
	}
	return Fingerprint(cs.PeerCertificates[0])
}

// ServerTLS exige certificado do cliente; verify decide se a impressão digital é aceita.
func (id *Identity) ServerTLS(verify func(fp string) error) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Cert},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{ALPN},
		MinVersion:   tls.VersionTLS13,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			fp, err := peerFP(raw)
			if err != nil {
				return err
			}
			return verify(fp)
		},
	}
}

// ClientTLS apresenta a identidade e confere a impressão digital do servidor
// (pinned vazio = aceitar qualquer, usado só no pareamento, que tem prova própria).
func (id *Identity) ClientTLS(pinned string) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{id.Cert},
		NextProtos:         []string{ALPN},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // a verificação real é o pinning abaixo
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			fp, err := peerFP(raw)
			if err != nil {
				return err
			}
			if pinned != "" && fp != pinned {
				return fmt.Errorf("impressão digital do ponto fixo não confere (esperada %s, recebida %s) — possível interceptação", Short(pinned), Short(fp))
			}
			return nil
		},
	}
}
