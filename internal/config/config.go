// Package config resolve diretórios de dados e guarda arquivos JSON.
//
// Escopo "máquina" (%ProgramData%\TelaRemota): identidade do computador,
// ponto fixo, agente e serviço — exige administrador.
// Escopo "usuário" (%APPDATA%\TelaRemota): identidade de quem visualiza.
// Para testes, TELAREMOTA_MAQUINA e TELAREMOTA_USUARIO sobrescrevem os caminhos.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const DefaultPort = 47070

func MachineDir() string {
	if d := os.Getenv("TELAREMOTA_MAQUINA"); d != "" {
		return d
	}
	if runtime.GOOS != "windows" {
		return "/var/lib/telaremota"
	}
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "TelaRemota")
}

func UserDir() string {
	if d := os.Getenv("TELAREMOTA_USUARIO"); d != "" {
		return d
	}
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "TelaRemota")
}

// Overridden indica que os diretórios foram trocados por variável de ambiente (modo teste).
func Overridden() bool { return os.Getenv("TELAREMOTA_MAQUINA") != "" }

// Client: a qual ponto fixo este dispositivo está pareado.
type Client struct {
	Hub     string `json:"hub"`      // host:porta
	HubFP   string `json:"hub_fp"`   // impressão digital fixada
	HubName string `json:"hub_name"` // nome do ponto fixo
	Name    string `json:"name"`     // nome deste dispositivo no ponto fixo
}

// Machine: o que o serviço executa.
type Machine struct {
	Hub   bool   `json:"hub"`   // atuar como ponto fixo
	Port  int    `json:"port"`  // porta UDP do ponto fixo
	UPnP  bool   `json:"upnp"`  // abrir a porta no roteador automaticamente
	Name  string `json:"name"`  // nome deste computador (quando é ponto fixo)
	Agent bool   `json:"agent"` // permitir que este computador seja acessado

	// Tailnet: entrar na rede Tailscale pelo nó embutido (sem instalar o Tailscale).
	Tailnet bool `json:"tailnet,omitempty"`
}

func Load(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func Exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

func Save(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Hostname em minúsculas, apenas [a-z0-9-].
func Hostname() string {
	h, _ := os.Hostname()
	return CleanName(h)
}

func CleanName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = out[:32]
	}
	if out == "" {
		out = "pc"
	}
	return out
}
