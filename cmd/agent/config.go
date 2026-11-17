package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Config é persistida em %APPDATA%\TelaRemota\config.json.
type Config struct {
	Server string `json:"server"`
	ID     string `json:"id,omitempty"`
	// Key identifica este dispositivo junto ao servidor (não é a senha de acesso).
	Key string `json:"key"`
	// Senha fixa (opcional): guardamos apenas o salt e a chave derivada.
	PassSalt string `json:"pass_salt,omitempty"`
	PassKey  string `json:"pass_key,omitempty"`
}

func defaultConfigPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "TelaRemota", "config.json"), nil
}

func loadConfig(path string) (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	return c, json.Unmarshal(b, c)
}

func saveConfig(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// wsURL converte o endereço do servidor no endpoint WebSocket.
func wsURL(server string) (string, error) {
	if !strings.Contains(server, "://") {
		server = "https://" + server
	}
	u, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("esquema inválido: %s", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/ws") {
		u.Path += "/ws"
	}
	return u.String(), nil
}

// viewerLink devolve o link que o técnico abre no navegador.
func viewerLink(server, id string) string {
	ws, err := wsURL(server)
	if err != nil {
		return ""
	}
	u, _ := url.Parse(ws)
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	u.Path = strings.TrimSuffix(u.Path, "ws")
	u.Fragment = id
	return u.String()
}

func formatID(id string) string {
	if len(id) != 9 {
		return id
	}
	return id[:3] + " " + id[3:6] + " " + id[6:]
}
