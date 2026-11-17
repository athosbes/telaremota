//go:build windows

package host

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	sas    = windows.NewLazySystemDLL("sas.dll")

	pOpenInputDesktop     = user32.NewProc("OpenInputDesktop")
	pSetThreadDesktop     = user32.NewProc("SetThreadDesktop")
	pCloseDesktop         = user32.NewProc("CloseDesktop")
	pGetUserObjectInfo    = user32.NewProc("GetUserObjectInformationW")
	pOpenWindowStation    = user32.NewProc("OpenWindowStationW")
	pSetProcessWinStation = user32.NewProc("SetProcessWindowStation")
	pSendSAS              = sas.NewProc("SendSAS")
)

const (
	maximumAllowed = 0x02000000
	uoiName        = 2
)

// AttachWinSta0 associa o processo à estação de janelas interativa.
// Necessário no processo auxiliar criado pelo serviço (que nasce na estação do serviço).
func AttachWinSta0() error {
	name, _ := windows.UTF16PtrFromString("WinSta0")
	h, _, err := pOpenWindowStation.Call(uintptr(unsafe.Pointer(name)), 0, maximumAllowed)
	if h == 0 {
		return err
	}
	if r, _, err := pSetProcessWinStation.Call(h); r == 0 {
		return err
	}
	return nil
}

// desktop acompanha a área de trabalho que recebe entrada (Default, Winlogon
// na tela de bloqueio/UAC...). Cada thread que captura ou injeta entrada
// precisa estar nela. Use somente em goroutine com runtime.LockOSThread.
type desktop struct {
	cur  uintptr
	name string
}

// sync troca a thread para a área de trabalho de entrada atual; devolve true se mudou.
func (d *desktop) sync() bool {
	h, _, _ := pOpenInputDesktop.Call(0, 0, maximumAllowed)
	if h == 0 {
		return false // sem permissão (fora do serviço, a tela de bloqueio é inacessível)
	}
	name := desktopName(h)
	if name == d.name {
		pCloseDesktop.Call(h)
		return false
	}
	if r, _, _ := pSetThreadDesktop.Call(h); r == 0 {
		pCloseDesktop.Call(h)
		return false
	}
	if d.cur != 0 {
		pCloseDesktop.Call(d.cur)
	}
	d.cur, d.name = h, name
	return true
}

func (d *desktop) close() {
	if d.cur != 0 {
		pCloseDesktop.Call(d.cur)
		d.cur = 0
	}
}

func desktopName(h uintptr) string {
	var buf [128]uint16
	var need uint32
	pGetUserObjectInfo.Call(h, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&need)))
	return windows.UTF16ToString(buf[:])
}

// sendCAD gera Ctrl+Alt+Del real (exige SYSTEM e a política SoftwareSASGeneration).
func sendCAD() error {
	if err := pSendSAS.Find(); err != nil {
		return errors.New("sas.dll indisponível")
	}
	pSendSAS.Call(0)
	return nil
}
