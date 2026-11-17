package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p := GeneratePassword()
		if len(p) != 11 || p[3] != '-' || p[7] != '-' {
			t.Fatalf("formato inválido: %q", p)
		}
		for _, c := range Normalize(p) {
			if !strings.ContainsRune(alphabet, c) {
				t.Fatalf("caractere fora do alfabeto: %q", p)
			}
		}
		if seen[p] {
			t.Fatalf("senha repetida: %q", p)
		}
		seen[p] = true
	}
}

func TestProofBindsFingerprints(t *testing.T) {
	k := DeriveKey(Normalize("ABC-def-234"), []byte("salt"))
	a := Proof(k, "viewer", "n", "AA", "BB")
	if !bytes.Equal(a, Proof(DeriveKey("abcdef234", []byte("salt")), "viewer", "n", "AA", "BB")) {
		t.Fatal("normalização deveria produzir a mesma chave")
	}
	if bytes.Equal(a, Proof(k, "viewer", "n", "AA", "CC")) {
		t.Fatal("prova deve depender das fingerprints")
	}
	if bytes.Equal(a, Proof(k, "agent", "n", "AA", "BB")) {
		t.Fatal("prova deve depender do papel")
	}
}

func TestFingerprint(t *testing.T) {
	sdp := "v=0\r\na=group:BUNDLE 0\r\na=fingerprint:sha-256 ab:cd:EF\r\na=setup:actpass\r\n"
	if got := Fingerprint(sdp); got != "AB:CD:EF" {
		t.Fatalf("got %q", got)
	}
}
