// Package auth implementa a autenticação ponta a ponta entre visualizador e
// agente.
//
// A senha nunca trafega. Depois que o canal WebRTC (DTLS) está aberto, o
// agente envia {salt, nonce}. Os dois lados derivam
//
//	K = PBKDF2-SHA256(senha, salt, Iterations)
//
// e trocam provas HMAC-SHA256(K, papel|nonce|fpViewer|fpAgente), onde fp são
// as impressões digitais DTLS lidas do SDP. Se alguém (inclusive o servidor de
// sinalização) fizer man-in-the-middle, cada lado enxerga fingerprints
// diferentes e as provas não batem. A autenticação é mútua: o visualizador
// também verifica que o agente conhece a senha.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
)

// Iterations do PBKDF2. O visualizador recusa valores menores.
const Iterations = 150_000

// alfabeto sem caracteres ambíguos (0/o, 1/l/i).
const alphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// GeneratePassword gera uma senha temporária de 9 caracteres (~44 bits),
// formatada como xxx-xxx-xxx.
func GeneratePassword() string {
	out := make([]byte, 0, 11)
	buf := make([]byte, 1)
	for len(out) < 11 {
		if len(out) == 3 || len(out) == 7 {
			out = append(out, '-')
			continue
		}
		rand.Read(buf)
		// amostragem por rejeição para distribuição uniforme
		if int(buf[0]) >= 256-256%len(alphabet) {
			continue
		}
		out = append(out, alphabet[int(buf[0])%len(alphabet)])
	}
	return string(out)
}

// Normalize deixa a senha em minúsculas e remove espaços e hífens.
// O visualizador aplica exatamente a mesma regra.
func Normalize(p string) string {
	p = strings.ToLower(p)
	return strings.NewReplacer("-", "", " ", "", "\t", "").Replace(p)
}

// DeriveKey calcula a chave a partir da senha (já normalizada).
func DeriveKey(password string, salt []byte) []byte {
	k, err := pbkdf2.Key(sha256.New, password, salt, Iterations, 32)
	if err != nil {
		panic(err)
	}
	return k
}

// Proof calcula a prova de um papel ("viewer" ou "agent").
func Proof(key []byte, role, nonce, fpViewer, fpAgent string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("telaremota/v1/" + role + "|" + nonce + "|" + fpViewer + "|" + fpAgent))
	return m.Sum(nil)
}

var fpRe = regexp.MustCompile(`(?im)^a=fingerprint:sha-256 ([0-9a-f:]+)\s*$`)

// Fingerprint extrai a fingerprint DTLS sha-256 de um SDP.
func Fingerprint(sdp string) string {
	m := fpRe.FindStringSubmatch(sdp)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// Random retorna n bytes aleatórios.
func Random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// RandomB64 retorna n bytes aleatórios em base64 padrão.
func RandomB64(n int) string { return base64.StdEncoding.EncodeToString(Random(n)) }
