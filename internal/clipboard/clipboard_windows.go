//go:build windows

// Package clipboard lê/escreve texto na área de transferência do Windows.
package clipboard

import (
	"errors"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pOpen    = user32.NewProc("OpenClipboard")
	pClose   = user32.NewProc("CloseClipboard")
	pEmpty   = user32.NewProc("EmptyClipboard")
	pGetData = user32.NewProc("GetClipboardData")
	pSetData = user32.NewProc("SetClipboardData")
	pIsAvail = user32.NewProc("IsClipboardFormatAvailable")
	pSeq     = user32.NewProc("GetClipboardSequenceNumber")
	pGAlloc  = kernel32.NewProc("GlobalAlloc")
	pGFree   = kernel32.NewProc("GlobalFree")
	pGLock   = kernel32.NewProc("GlobalLock")
	pGUnlock = kernel32.NewProc("GlobalUnlock")
	pGSize   = kernel32.NewProc("GlobalSize")
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
	// MaxLen limita o texto sincronizado (em caracteres UTF-16).
	MaxLen = 1 << 20
)

// ptr converte um endereço retornado pela API em unsafe.Pointer.
func ptr(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

func open() error {
	for range 20 {
		if r, _, _ := pOpen.Call(0); r != 0 {
			return nil
		}
		time.Sleep(15 * time.Millisecond)
	}
	return errors.New("área de transferência ocupada")
}

// Seq muda sempre que o conteúdo da área de transferência muda.
func Seq() uint32 {
	r, _, _ := pSeq.Call()
	return uint32(r)
}

// Get retorna o texto atual ("" se não houver texto).
func Get() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, _ := pIsAvail.Call(cfUnicodeText); r == 0 {
		return "", nil
	}
	if err := open(); err != nil {
		return "", err
	}
	defer pClose.Call()
	h, _, _ := pGetData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}
	p, _, _ := pGLock.Call(h)
	if p == 0 {
		return "", nil
	}
	defer pGUnlock.Call(h)
	size, _, _ := pGSize.Call(h)
	n := min(int(size/2), MaxLen)
	u := unsafe.Slice((*uint16)(ptr(p)), n)
	for i, c := range u {
		if c == 0 {
			u = u[:i]
			break
		}
	}
	return strings.ReplaceAll(windows.UTF16ToString(u), "\r\n", "\n"), nil
}

// Set substitui o conteúdo por texto.
func Set(text string) error {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\x00", ""), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\n", "\r\n")
	u, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	if len(u) > MaxLen {
		return errors.New("texto grande demais")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := open(); err != nil {
		return err
	}
	defer pClose.Call()
	pEmpty.Call()
	h, _, _ := pGAlloc.Call(gmemMoveable, uintptr(len(u)*2))
	if h == 0 {
		return errors.New("GlobalAlloc falhou")
	}
	p, _, _ := pGLock.Call(h)
	if p == 0 {
		pGFree.Call(h)
		return errors.New("GlobalLock falhou")
	}
	copy(unsafe.Slice((*uint16)(ptr(p)), len(u)), u)
	pGUnlock.Call(h)
	if r, _, _ := pSetData.Call(cfUnicodeText, h); r == 0 {
		pGFree.Call(h)
		return errors.New("SetClipboardData falhou")
	}
	return nil
}
