//go:build windows

package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInDir(t *testing.T) {
	dir := `C:\Program Files\TelaRemota`
	for path, want := range map[string]bool{
		dir + `\telaremota.exe`:               true,
		dir + `\sub\x.exe`:                    true,
		`C:\Program Files\TelaRemota2\t.exe`:  false,
		`C:\Users\x\Downloads\telaremota.exe`: false,
		`C:\Program Files\..TelaRemota\x.exe`: false,
	} {
		if got := inDir(path, dir); got != want {
			t.Errorf("inDir(%q) = %v, quero %v", path, got, want)
		}
	}
}

func TestCreateShortcut(t *testing.T) {
	dir := t.TempDir()
	lnk := filepath.Join(dir, "Teste TelaRemota.lnk")
	target := filepath.Join(dir, "com espaço", "telaremota.exe")
	if err := createShortcut(lnk, target, "painel"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(lnk); err != nil || fi.Size() == 0 {
		t.Fatalf("atalho não criado: %v", err)
	}
}

func TestRemoveAfterExit(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "programa"), filepath.Join(base, "dados com espaço")
	for _, d := range []string{a, b} {
		os.MkdirAll(filepath.Join(d, "sub"), 0o755)
		os.WriteFile(filepath.Join(d, "sub", "f.txt"), []byte("x"), 0o644)
	}
	if err := removeAfterExit([]string{a, b}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, ea := os.Stat(a)
		_, eb := os.Stat(b)
		if os.IsNotExist(ea) && os.IsNotExist(eb) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("pastas não foram apagadas")
}
