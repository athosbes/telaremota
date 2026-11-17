//go:build windows

// Package input injeta mouse e teclado via SendInput.
package input

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var pSendInput = windows.NewLazySystemDLL("user32.dll").NewProc("SendInput")

const (
	typeMouse    = 0
	typeKeyboard = 1

	mMove     = 0x0001
	mLDown    = 0x0002
	mLUp      = 0x0004
	mRDown    = 0x0008
	mRUp      = 0x0010
	mMDown    = 0x0020
	mMUp      = 0x0040
	mXDown    = 0x0080
	mXUp      = 0x0100
	mWheel    = 0x0800
	mHWheel   = 0x1000
	mVirtual  = 0x4000
	mAbsolute = 0x8000

	kExtended = 0x0001
	kUp       = 0x0002
	kScan     = 0x0008

	inputSize = 40 // sizeof(INPUT) em 64 bits
)

type mouseInput struct {
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	Extra     uintptr
}

type keybdInput struct {
	Vk, Scan uint16
	Flags    uint32
	Time     uint32
	Extra    uintptr
	_        [8]byte // completa a union até 32 bytes
}

type mouseEvt struct {
	Type uint32
	_    uint32
	Mi   mouseInput
}

type keyEvt struct {
	Type uint32
	_    uint32
	Ki   keybdInput
}

func init() {
	if unsafe.Sizeof(mouseEvt{}) != inputSize || unsafe.Sizeof(keyEvt{}) != inputSize {
		panic("layout de INPUT inesperado (somente 64 bits é suportado)")
	}
}

func sendMouse(ev *mouseEvt) { pSendInput.Call(1, uintptr(unsafe.Pointer(ev)), inputSize) }
func sendKey(ev *keyEvt)     { pSendInput.Call(1, uintptr(unsafe.Pointer(ev)), inputSize) }

// Injector rastreia teclas/botões pressionados para soltá-los ao fim da sessão.
type Injector struct {
	mu      sync.Mutex
	keys    map[string]bool
	scans   map[uint16]bool
	buttons map[int]bool
}

func New() *Injector {
	return &Injector{keys: map[string]bool{}, scans: map[uint16]bool{}, buttons: map[int]bool{}}
}

// MouseMove move o cursor para (x,y) no monitor principal de tamanho w×h.
func (in *Injector) MouseMove(x, y, w, h int) {
	if w < 2 || h < 2 {
		return
	}
	x, y = clamp(x, 0, w-1), clamp(y, 0, h-1)
	ev := mouseEvt{Type: typeMouse, Mi: mouseInput{
		Dx:    int32((x*65536 + w - 1) / w),
		Dy:    int32((y*65536 + h - 1) / h),
		Flags: mMove | mAbsolute,
	}}
	sendMouse(&ev)
}

// MouseMoveVirtual move o cursor para (x,y) relativo ao canto da área de
// trabalho virtual de tamanho w×h (todos os monitores).
func (in *Injector) MouseMoveVirtual(x, y, w, h int) {
	if w < 2 || h < 2 {
		return
	}
	x, y = clamp(x, 0, w-1), clamp(y, 0, h-1)
	ev := mouseEvt{Type: typeMouse, Mi: mouseInput{
		Dx:    int32((x*65536 + w - 1) / w),
		Dy:    int32((y*65536 + h - 1) / h),
		Flags: mMove | mAbsolute | mVirtual,
	}}
	sendMouse(&ev)
}

// Button pressiona/solta um botão (numeração de MouseEvent.button do navegador).
func (in *Injector) Button(b int, down bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.button(b, down)
}

func (in *Injector) button(b int, down bool) {
	var flags, data uint32
	pick := func(d, u uint32) uint32 {
		if down {
			return d
		}
		return u
	}
	switch b {
	case 0:
		flags = pick(mLDown, mLUp)
	case 1:
		flags = pick(mMDown, mMUp)
	case 2:
		flags = pick(mRDown, mRUp)
	case 3, 4:
		flags, data = pick(mXDown, mXUp), uint32(b-2)
	default:
		return
	}
	if down {
		in.buttons[b] = true
	} else {
		delete(in.buttons, b)
	}
	ev := mouseEvt{Type: typeMouse, Mi: mouseInput{Flags: flags, MouseData: data}}
	sendMouse(&ev)
}

// Wheel rola verticalmente (dy>0 = para cima) e horizontalmente. 120 = 1 "dente".
func (in *Injector) Wheel(dy, dx int) {
	if dy != 0 {
		ev := mouseEvt{Type: typeMouse, Mi: mouseInput{Flags: mWheel, MouseData: uint32(int32(clamp(dy, -2400, 2400)))}}
		sendMouse(&ev)
	}
	if dx != 0 {
		ev := mouseEvt{Type: typeMouse, Mi: mouseInput{Flags: mHWheel, MouseData: uint32(int32(clamp(dx, -2400, 2400)))}}
		sendMouse(&ev)
	}
}

// Key pressiona/solta uma tecla identificada por KeyboardEvent.code.
// Usa scancodes (tecla física), então o layout do teclado remoto é respeitado.
func (in *Injector) Key(code string, down bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.key(code, down)
}

func (in *Injector) key(code string, down bool) {
	ev := keyEvt{Type: typeKeyboard}
	if vk, ok := vkOnly[code]; ok {
		ev.Ki.Vk = vk
	} else if sc, ok := scanCodes[code]; ok {
		ev.Ki.Scan = sc & 0xFF
		ev.Ki.Flags = kScan
		if sc&0xE000 == 0xE000 {
			ev.Ki.Flags |= kExtended
		}
	} else {
		return
	}
	if down {
		in.keys[code] = true
	} else {
		ev.Ki.Flags |= kUp
		delete(in.keys, code)
	}
	sendKey(&ev)
}

// ScanPause é o código especial usado para a tecla Pause.
const ScanPause = 0xE11D

// Scan pressiona/solta uma tecla pelo scancode "set 1" (0xE0xx = estendida).
func (in *Injector) Scan(sc uint16, down bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.scan(sc, down)
}

func (in *Injector) scan(sc uint16, down bool) {
	ev := keyEvt{Type: typeKeyboard}
	if sc == ScanPause {
		ev.Ki.Vk = 0x13
	} else {
		ev.Ki.Scan = sc & 0xFF
		ev.Ki.Flags = kScan
		if sc&0xFF00 == 0xE000 {
			ev.Ki.Flags |= kExtended
		}
	}
	if down {
		in.scans[sc] = true
	} else {
		ev.Ki.Flags |= kUp
		delete(in.scans, sc)
	}
	sendKey(&ev)
}

// ReleaseAll solta tudo que ficou pressionado (ex.: conexão caiu no meio de um Ctrl).
func (in *Injector) ReleaseAll() {
	in.mu.Lock()
	defer in.mu.Unlock()
	for k := range in.keys {
		in.key(k, false)
	}
	for sc := range in.scans {
		in.scan(sc, false)
	}
	for b := range in.buttons {
		in.button(b, false)
	}
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }
