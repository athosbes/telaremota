package main

import (
	"bytes"
	"testing"
)

func TestDPAPIRoundTrip(t *testing.T) {
	in := []byte("senha-secreta-123")
	enc, err := dpapi(in, true)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, in) {
		t.Fatal("texto cifrado contém a senha em claro")
	}
	out, err := dpapi(enc, false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, out) {
		t.Fatalf("got %q", out)
	}
}

func TestRememberPassword(t *testing.T) {
	t.Setenv("TELAREMOTA_USUARIO", t.TempDir())
	rememberPassword("casa", "abc12345")
	if got := rememberedPassword("casa"); got != "abc12345" {
		t.Fatalf("got %q", got)
	}
	rememberPassword("casa", "")
	if got := rememberedPassword("casa"); got != "" {
		t.Fatalf("deveria ter apagado, got %q", got)
	}
}
