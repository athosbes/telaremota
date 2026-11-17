package hub

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"telaremota/internal/auth"
	"telaremota/internal/config"
)

// Device é um dispositivo pareado.
type Device struct {
	FP    string    `json:"fp"`
	Name  string    `json:"name"`
	Kind  string    `json:"kind"` // "maquina" | "usuario"
	Added time.Time `json:"added"`
	Admin bool      `json:"admin,omitempty"` // pode gerar convites e revogar dispositivos remotamente
}

// devices é persistido em hub/devices.json e recarregado quando o arquivo muda
// (assim "telaremota remover" funciona com o serviço rodando).
type devices struct {
	mu    sync.Mutex
	path  string
	mtime time.Time
	list  []Device
}

func DevicesPath(dir string) string { return filepath.Join(dir, "hub", "devices.json") }
func InvitePath(dir string) string  { return filepath.Join(dir, "hub", "invite.json") }
func StatusPath(dir string) string  { return filepath.Join(dir, "hub", "status.json") }

func (d *devices) reload() (changed bool) {
	st, err := os.Stat(d.path)
	if err != nil {
		if len(d.list) > 0 {
			d.list, d.mtime = nil, time.Time{}
			return true
		}
		return false
	}
	if st.ModTime().Equal(d.mtime) {
		return false
	}
	var list []Device
	if config.Load(d.path, &list) == nil {
		d.list, d.mtime = list, st.ModTime()
		return true
	}
	return false
}

func (d *devices) find(fp string) (Device, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reload()
	for _, x := range d.list {
		if x.FP == fp {
			return x, true
		}
	}
	return Device{}, false
}

// add registra o dispositivo, garantindo nome único.
func (d *devices) add(dev Device, reserved string) (Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reload()
	taken := map[string]bool{reserved: true}
	out := d.list[:0:0]
	for _, x := range d.list {
		if x.FP == dev.FP {
			continue // repareamento: substitui
		}
		taken[x.Name] = true
		out = append(out, x)
	}
	base := dev.Name
	for i := 2; taken[dev.Name]; i++ {
		dev.Name = fmt.Sprintf("%s-%d", base, i)
	}
	out = append(out, dev)
	if err := config.Save(d.path, out); err != nil {
		return dev, err
	}
	d.list = out
	if st, err := os.Stat(d.path); err == nil {
		d.mtime = st.ModTime()
	}
	return dev, nil
}

// ListDevices / RemoveDevice são usados pela linha de comando.
func ListDevices(dir string) ([]Device, error) {
	var list []Device
	err := config.Load(DevicesPath(dir), &list)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, err
}

func RemoveDevice(dir, name string) error {
	list, err := ListDevices(dir)
	if err != nil {
		return err
	}
	out := list[:0]
	found := false
	for _, x := range list {
		if x.Name == name {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		return fmt.Errorf("dispositivo %q não encontrado", name)
	}
	return config.Save(DevicesPath(dir), out)
}

// Invite é um convite de pareamento: o código em si nunca é gravado.
type Invite struct {
	Salt     string    `json:"salt"`
	Key      string    `json:"key"`
	Expires  time.Time `json:"expires"`
	Uses     int       `json:"uses"`
	Failures int       `json:"failures"`
	Admin    bool      `json:"admin,omitempty"` // usuários pareados com ele viram administradores
}

const maxInviteFailures = 5

// NewInvite gera um código e grava o convite. Retorna o código para exibir.
func NewInvite(dir string, uses int, ttl time.Duration, admin bool) (string, error) {
	code := auth.GeneratePassword()
	salt := auth.Random(16)
	inv := Invite{
		Salt:    base64.StdEncoding.EncodeToString(salt),
		Key:     base64.StdEncoding.EncodeToString(auth.DeriveKey(auth.Normalize(code), salt)),
		Expires: time.Now().Add(ttl),
		Uses:    uses,
		Admin:   admin,
	}
	return code, config.Save(InvitePath(dir), inv)
}

func loadInvite(dir string) (*Invite, error) {
	var inv Invite
	if err := config.Load(InvitePath(dir), &inv); err != nil {
		return nil, errors.New("nenhum convite ativo")
	}
	if time.Now().After(inv.Expires) || inv.Uses <= 0 || inv.Failures >= maxInviteFailures {
		os.Remove(InvitePath(dir))
		return nil, errors.New("convite expirado ou já utilizado")
	}
	return &inv, nil
}

func saveInvite(dir string, inv *Invite) {
	if inv.Uses <= 0 || inv.Failures >= maxInviteFailures {
		os.Remove(InvitePath(dir))
		return
	}
	config.Save(InvitePath(dir), inv)
}

// Status é gravado pelo ponto fixo para o comando "convite" mostrar endereços.
type Status struct {
	Name     string    `json:"name"`
	Port     int       `json:"port"`
	PublicIP string    `json:"public_ip,omitempty"`
	IPv6     []string  `json:"ipv6,omitempty"`
	LAN      []string  `json:"lan,omitempty"`
	VPN      []string  `json:"vpn,omitempty"` // Tailscale/ZeroTier
	UPnP     bool      `json:"upnp"`
	FP       string    `json:"fp"`
	Updated  time.Time `json:"updated"`
}
