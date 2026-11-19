//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Version é a versão mostrada em Aplicativos instalados (definida pelo main).
var Version = "dev"

const uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\TelaRemota`

// shortcutPath é o atalho do menu Iniciar para todos os usuários.
func shortcutPath() string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_CommonPrograms, 0)
	if err != nil {
		dir = filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs`)
	}
	return filepath.Join(dir, "TelaRemota.lnk")
}

// registerApp cria o atalho no menu Iniciar e a entrada em Configurações →
// Aplicativos instalados, que chama "telaremota desinstalar". É o que faz o
// executável único se comportar como um programa instalado; falhas aqui não
// impedem o serviço de funcionar, então são só avisadas.
func registerApp(exe string) error {
	var errs []string
	if err := createShortcut(shortcutPath(), exe, "painel"); err != nil {
		errs = append(errs, "atalho no menu Iniciar: "+err.Error())
	}
	if err := writeUninstallEntry(exe); err != nil {
		errs = append(errs, "entrada em Aplicativos instalados: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func writeUninstallEntry(exe string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	size := uint32(0)
	if fi, err := os.Stat(exe); err == nil {
		size = uint32(fi.Size() / 1024)
	}
	q := `"` + exe + `"`
	for name, v := range map[string]string{
		"DisplayName":          "TelaRemota",
		"DisplayVersion":       strings.TrimPrefix(Version, "v"),
		"Publisher":            "athosbes",
		"DisplayIcon":          exe + ",0",
		"InstallLocation":      filepath.Dir(exe),
		"UninstallString":      q + " desinstalar",
		"QuietUninstallString": q + " desinstalar -silencioso",
		"URLInfoAbout":         "https://github.com/athosbes/telaremota",
		"HelpLink":             "https://github.com/athosbes/telaremota/issues",
		"InstallDate":          time.Now().Format("20060102"),
	} {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	k.SetDWordValue("EstimatedSize", size)
	k.SetDWordValue("NoModify", 1)
	return k.SetDWordValue("NoRepair", 1)
}

// createShortcut cria um atalho .lnk pelo WScript.Shell (os caminhos vão por
// variáveis de ambiente, sem montar código com eles).
func createShortcut(lnk, target, args string) error {
	const script = `$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:TR_LNK)
$s.TargetPath = $env:TR_EXE
$s.Arguments = $env:TR_ARGS
$s.WorkingDirectory = Split-Path $env:TR_EXE
$s.IconLocation = "$env:TR_EXE,0"
$s.Description = 'Acesso remoto TelaRemota'
$s.Save()`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "TR_LNK="+lnk, "TR_EXE="+target, "TR_ARGS="+args)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsInstalled informa se o programa está registrado em Aplicativos instalados.
func IsInstalled() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

// UninstallApp remove tudo o que a instalação criou: serviço, regra de
// firewall, atalho, entrada em Aplicativos instalados e a pasta do programa.
// Com removeData, apaga também os diretórios em dataDirs (identidade e
// pareamentos). Se este executável estiver na pasta do programa, ela é
// apagada logo depois que o processo terminar.
func UninstallApp(removeData bool, dataDirs ...string) error {
	if err := Uninstall(); err != nil && err != ErrNotInstalled {
		return err
	}
	os.Remove(shortcutPath())
	registry.DeleteKey(registry.LOCAL_MACHINE, uninstallKey)

	dirs := []string{InstallDir()}
	if removeData {
		dirs = append(dirs, dataDirs...)
	}
	self, _ := os.Executable()
	var later []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if inDir(self, d) {
			later = append(later, d)
			continue
		}
		if err := os.RemoveAll(d); err != nil {
			later = append(later, d) // algum arquivo em uso: tenta de novo em seguida
		}
	}
	if len(later) > 0 {
		return removeAfterExit(later)
	}
	return nil
}

func inDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// removeAfterExit apaga as pastas por um cmd.exe separado, alguns segundos
// depois (um executável não consegue apagar a si mesmo enquanto roda).
func removeAfterExit(dirs []string) error {
	var b strings.Builder
	b.WriteString(`ping -n 4 127.0.0.1 >nul`)
	for _, d := range dirs {
		b.WriteString(` & rmdir /s /q "` + d + `"`)
	}
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `/d /c "` + b.String() + `"`,
		HideWindow:    true,
		CreationFlags: 0x08000000 | 0x00000008, // CREATE_NO_WINDOW | DETACHED_PROCESS
	}
	cmd.Dir = os.Getenv("SystemRoot") // não prender a pasta que vai ser apagada
	return cmd.Start()
}
