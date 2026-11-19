//go:build windows

// Package service instala/executa o TelaRemota como serviço do Windows e
// cria o processo auxiliar que roda na sessão do usuário logado.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const Name = "TelaRemota"

// ErrNotInstalled indica que o serviço não está registrado.
var ErrNotInstalled = errors.New("serviço não está instalado")

// IsService informa se o processo foi iniciado pelo gerenciador de serviços.
func IsService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

// IsElevated informa se o processo roda como administrador.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

type handler struct{ run func(ctx context.Context) }

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.run(ctx); close(done) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
loop:
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				break loop
			}
		case <-done:
			break loop
		}
	}
	st <- svc.Status{State: svc.StopPending}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	return false, 0
}

// Run executa run como serviço até o Windows pedir para parar.
func Run(run func(ctx context.Context)) error { return svc.Run(Name, &handler{run}) }

// InstallDir é onde o executável é copiado.
func InstallDir() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "TelaRemota")
}

// Install copia o executável, registra o serviço e o inicia.
func Install(dataDir string, firewallPort int) (string, error) {
	src, err := os.Executable()
	if err != nil {
		return "", err
	}
	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("gerenciador de serviços: %w (execute como administrador)", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil { // reinstalação
		stop(s)
		s.Delete()
		s.Close()
		time.Sleep(time.Second)
	}

	dir := InstallDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	exe := filepath.Join(dir, "telaremota.exe")
	if !samePath(src, exe) {
		if err := copyFile(src, exe); err != nil {
			return "", fmt.Errorf("copiar executável: %w", err)
		}
	}

	s, err := m.CreateService(Name, exe, mgr.Config{
		StartType:   mgr.StartAutomatic,
		DisplayName: "TelaRemota",
		Description: "Acesso remoto TelaRemota (ponto fixo e/ou agente).",
	}, "servico", "executar")
	if err != nil {
		return "", err
	}
	defer s.Close()
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 24*60*60)

	// Permite que serviços gerem Ctrl+Alt+Del (SendSAS).
	if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.SET_VALUE); err == nil {
		k.SetDWordValue("SoftwareSASGeneration", 3)
		k.Close()
	}
	// Dados da máquina: só SYSTEM e Administradores.
	os.MkdirAll(dataDir, 0o700)
	exec.Command("icacls", dataDir, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F").Run()
	if firewallPort > 0 {
		exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=TelaRemota").Run()
		exec.Command("netsh", "advfirewall", "firewall", "add", "rule", "name=TelaRemota", "dir=in", "action=allow",
			"protocol=UDP", fmt.Sprintf("localport=%d", firewallPort), "program="+exe, "enable=yes").Run()
	}
	if err := s.Start(); err != nil {
		return exe, fmt.Errorf("serviço instalado, mas não iniciou: %w", err)
	}
	if err := registerApp(exe); err != nil {
		fmt.Fprintln(os.Stderr, "aviso:", err) // o serviço já está de pé; só o atalho/entrada falharam
	}
	return exe, nil
}

// Uninstall para e remove o serviço e a regra de firewall.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("gerenciador de serviços: %w (execute como administrador)", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return ErrNotInstalled
	}
	defer s.Close()
	stop(s)
	exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name=TelaRemota").Run()
	return s.Delete()
}

func stop(s *mgr.Service) {
	st, err := s.Control(svc.Stop)
	for i := 0; err == nil && st.State != svc.Stopped && i < 40; i++ {
		time.Sleep(250 * time.Millisecond)
		st, err = s.Query()
	}
}

func samePath(a, b string) bool {
	a, _ = filepath.Abs(a)
	b, _ = filepath.Abs(b)
	return filepath.Clean(a) == filepath.Clean(b) || (len(a) == len(b) && equalFold(a, b))
}

func equalFold(a, b string) bool {
	for i := range a {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 32
		}
		if 'A' <= y && y <= 'Z' {
			y += 32
		}
		if x != y {
			return false
		}
	}
	return true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	out.Close()
	os.Remove(dst)
	return os.Rename(tmp, dst)
}

// Helper é o processo auxiliar rodando na sessão do console.
type Helper struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}

// StartHelper inicia este executável com args na sessão interativa ativa,
// como SYSTEM (necessário para tela de bloqueio, UAC e Ctrl+Alt+Del).
func StartHelper(args ...string) (*Helper, error) {
	sid := windows.WTSGetActiveConsoleSessionId()
	if sid == 0xFFFFFFFF {
		return nil, errors.New("nenhuma sessão de console ativa")
	}
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ALL_ACCESS, &tok); err != nil {
		return nil, err
	}
	defer tok.Close()
	var dup windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.MAXIMUM_ALLOWED, nil, windows.SecurityIdentification, windows.TokenPrimary, &dup); err != nil {
		return nil, err
	}
	defer dup.Close()
	if err := windows.SetTokenInformation(dup, windows.TokenSessionId, (*byte)(unsafe.Pointer(&sid)), 4); err != nil {
		return nil, fmt.Errorf("SetTokenInformation: %w", err)
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Token:         syscall.Token(dup),
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Helper{Cmd: cmd, Stdin: stdin, Stdout: stdout}, nil
}
