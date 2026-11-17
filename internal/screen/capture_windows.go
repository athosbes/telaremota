//go:build windows

// Package screen captura um monitor (ou a área de trabalho virtual inteira)
// via GDI (BitBlt), incluindo o cursor do mouse. Retorna pixels BGRA de cima
// para baixo.
package screen

import (
	"errors"
	"os"
	"slices"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	pGetDC              = user32.NewProc("GetDC")
	pReleaseDC          = user32.NewProc("ReleaseDC")
	pGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	pGetCursorInfo      = user32.NewProc("GetCursorInfo")
	pGetIconInfo        = user32.NewProc("GetIconInfo")
	pDrawIconEx         = user32.NewProc("DrawIconEx")
	pSetDpiCtx          = user32.NewProc("SetProcessDpiAwarenessContext")
	pSetDPIAware        = user32.NewProc("SetProcessDPIAware")
	pCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
	pBitBlt             = gdi32.NewProc("BitBlt")
	pEnumDisplayMons    = user32.NewProc("EnumDisplayMonitors")
	pGdiFlush           = gdi32.NewProc("GdiFlush")
)

const (
	smCxScreen  = 0
	smCyScreen  = 1
	smXVirtual  = 76
	smYVirtual  = 77
	smCxVirtual = 78
	smCyVirtual = 79
	srcCopy     = 0x00CC0020
	diNormal    = 3
)

// SetDPIAware faz o processo enxergar pixels físicos (sem escala do Windows).
// Deve ser chamado antes de qualquer outra chamada de interface.
func SetDPIAware() {
	if pSetDpiCtx.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
		if r, _, _ := pSetDpiCtx.Call(^uintptr(3)); r != 0 {
			return
		}
	}
	pSetDPIAware.Call()
}

// Size retorna o tamanho do monitor principal em pixels físicos.
func Size() (int, int) {
	w, _, _ := pGetSystemMetrics.Call(smCxScreen)
	h, _, _ := pGetSystemMetrics.Call(smCyScreen)
	return int(int32(w)), int(int32(h))
}

// VirtualScreen retorna a área de trabalho virtual (todos os monitores).
func VirtualScreen() (x, y, w, h int) {
	m := func(i uintptr) int { r, _, _ := pGetSystemMetrics.Call(i); return int(int32(r)) }
	return m(smXVirtual), m(smYVirtual), m(smCxVirtual), m(smCyVirtual)
}

// Monitor é um retângulo da área de trabalho virtual, em pixels físicos.
type Monitor struct {
	X, Y, W, H int
	Primary    bool
}

type rect struct{ L, T, R, B int32 }

var (
	monMu   sync.Mutex
	monList []Monitor
	monCB   = windows.NewCallback(func(h, dc uintptr, r *rect, _ uintptr) uintptr {
		monList = append(monList, Monitor{
			X: int(r.L), Y: int(r.T), W: int(r.R - r.L), H: int(r.B - r.T),
			Primary: r.L == 0 && r.T == 0, // o principal sempre começa na origem
		})
		return 1
	})
)

// Monitors lista os monitores da esquerda para a direita (depois de cima para baixo).
func Monitors() []Monitor {
	monMu.Lock()
	defer monMu.Unlock()
	monList = nil
	pEnumDisplayMons.Call(0, 0, monCB, 0)
	out := slices.Clone(monList)
	slices.SortFunc(out, func(a, b Monitor) int {
		if a.X != b.X {
			return a.X - b.X
		}
		return a.Y - b.Y
	})
	return out
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

type point struct{ X, Y int32 }

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor uintptr
	Pos    point
}

type iconInfo struct {
	FIcon int32
	XHot  uint32
	YHot  uint32
	Mask  uintptr
	Color uintptr
}

// Capturer mantém DCs e o bitmap reaproveitados entre quadros.
type Capturer struct {
	x, y, w, h                 int  // região capturada (coordenadas virtuais)
	fixed                      bool // região definida por SetRegion (senão, o monitor principal)
	screenDC, memDC, bmp, prev uintptr
	bits                       unsafe.Pointer
	hot                        map[uintptr][2]int32

	dx      []*dupl   // captura por DXGI, um por monitor da região (nil = GDI)
	dxRetry time.Time // próxima tentativa de abrir a DXGI
	clean   []byte    // último quadro da DXGI, sem o cursor
}

// noDXGI força a captura GDI (diagnóstico): TELAREMOTA_GDI=1.
var noDXGI = os.Getenv("TELAREMOTA_GDI") == "1"

// NewCapturer captura o monitor principal até que SetRegion escolha outra região.
func NewCapturer() (*Capturer, error) {
	c := &Capturer{hot: map[uintptr][2]int32{}}
	c.w, c.h = Size()
	return c, c.init()
}

// SetRegion passa a capturar o retângulo (x,y,w,h) da área de trabalho virtual.
func (c *Capturer) SetRegion(x, y, w, h int) {
	c.fixed = true
	if w != c.w || h != c.h {
		c.release() // bitmap de outro tamanho; recriado no próximo Capture
	} else if x != c.x || y != c.y {
		c.closeDXGI()
	}
	c.x, c.y, c.w, c.h = x, y, w, h
}

func (c *Capturer) init() error {
	w, h := c.w, c.h
	if w <= 0 || h <= 0 {
		return errors.New("tamanho de tela inválido")
	}
	sdc, _, _ := pGetDC.Call(0)
	if sdc == 0 {
		return errors.New("GetDC falhou")
	}
	mdc, _, _ := pCreateCompatibleDC.Call(sdc)
	if mdc == 0 {
		pReleaseDC.Call(0, sdc)
		return errors.New("CreateCompatibleDC falhou")
	}
	bi := bitmapInfo{Header: bitmapInfoHeader{
		Width: int32(w), Height: -int32(h), // altura negativa = top-down
		Planes: 1, BitCount: 32,
	}}
	bi.Header.Size = uint32(unsafe.Sizeof(bi.Header))
	var bits unsafe.Pointer
	bmp, _, _ := pCreateDIBSection.Call(mdc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		pDeleteDC.Call(mdc)
		pReleaseDC.Call(0, sdc)
		return errors.New("CreateDIBSection falhou")
	}
	prev, _, _ := pSelectObject.Call(mdc, bmp)
	c.screenDC, c.memDC, c.bmp, c.prev, c.bits = sdc, mdc, bmp, prev, bits
	return nil
}

func (c *Capturer) closeDXGI() {
	for _, d := range c.dx {
		d.close()
	}
	c.dx = nil
	c.dxRetry = time.Time{}
}

func (c *Capturer) release() {
	c.closeDXGI()
	if c.memDC != 0 {
		pSelectObject.Call(c.memDC, c.prev)
		pDeleteObject.Call(c.bmp)
		pDeleteDC.Call(c.memDC)
	}
	if c.screenDC != 0 {
		pReleaseDC.Call(0, c.screenDC)
	}
	c.screenDC, c.memDC, c.bmp, c.prev, c.bits = 0, 0, 0, 0, nil
}

// Close libera os recursos GDI.
func (c *Capturer) Close() { c.release() }

// Capture copia a tela. O slice retornado é reutilizado no próximo quadro.
func (c *Capturer) Capture() (pix []byte, w, h int, err error) {
	if !c.fixed {
		if sw, sh := Size(); sw != c.w || sh != c.h {
			c.release()
			c.x, c.y, c.w, c.h = 0, 0, sw, sh
		}
	}
	if c.memDC == 0 {
		if err := c.init(); err != nil {
			return nil, 0, 0, err
		}
	}
	if c.captureDXGI() {
		c.drawCursor()
		pGdiFlush.Call()
		return unsafe.Slice((*byte)(c.bits), c.w*c.h*4), c.w, c.h, nil
	}
	r, _, _ := pBitBlt.Call(c.memDC, 0, 0, uintptr(c.w), uintptr(c.h), c.screenDC, uintptr(c.x), uintptr(c.y), srcCopy)
	if r == 0 {
		c.release() // ex.: troca de desktop (tela de bloqueio/UAC); recria no próximo quadro
		return nil, 0, 0, errors.New("BitBlt falhou")
	}
	c.drawCursor()
	pGdiFlush.Call()
	return unsafe.Slice((*byte)(c.bits), c.w*c.h*4), c.w, c.h, nil
}

// captureDXGI tenta obter o quadro pela GPU; false = usar GDI neste quadro.
func (c *Capturer) captureDXGI() bool {
	if noDXGI {
		return false
	}
	if c.dx == nil {
		if time.Now().Before(c.dxRetry) || !c.openDXGI() {
			c.dxRetry = time.Now().Add(5 * time.Second)
			return false
		}
	}
	stride := c.w * 4
	for _, d := range c.dx {
		off := (d.y-c.y)*stride + (d.x-c.x)*4
		if err := d.frame(c.clean[off:], stride, 0); err != nil && err != errNoChange {
			// acesso perdido (troca de área de trabalho, modo de vídeo...)
			c.closeDXGI()
			c.dxRetry = time.Now().Add(time.Second)
			return false
		}
	}
	copy(unsafe.Slice((*byte)(c.bits), c.w*c.h*4), c.clean)
	return true
}

// openDXGI abre uma duplicação para cada monitor dentro da região; falha se
// algum monitor da região não puder ser duplicado.
func (c *Capturer) openDXGI() bool {
	var dx []*dupl
	for _, m := range Monitors() {
		if m.X < c.x || m.Y < c.y || m.X+m.W > c.x+c.w || m.Y+m.H > c.y+c.h {
			if m.X < c.x+c.w && m.Y < c.y+c.h && m.X+m.W > c.x && m.Y+m.H > c.y {
				dx = append(dx, nil) // monitor cortado pela região: só GDI
			}
			continue
		}
		d, err := newDupl(m.X, m.Y, m.W, m.H)
		dx = append(dx, d)
		if err != nil {
			break
		}
	}
	if len(dx) == 0 || slices.Contains(dx, nil) {
		for _, d := range dx {
			if d != nil {
				d.close()
			}
		}
		return false
	}
	// A DXGI só entrega a imagem quando algo é redesenhado: a imagem inicial
	// vem do GDI (sem o cursor, que é desenhado a cada quadro).
	if r, _, _ := pBitBlt.Call(c.memDC, 0, 0, uintptr(c.w), uintptr(c.h), c.screenDC, uintptr(c.x), uintptr(c.y), srcCopy); r == 0 {
		for _, d := range dx {
			d.close()
		}
		return false
	}
	pGdiFlush.Call()
	c.dx = dx
	c.clean = slices.Clone(unsafe.Slice((*byte)(c.bits), c.w*c.h*4))
	return true
}

// UsingGPU diz se a captura está usando a DXGI.
func (c *Capturer) UsingGPU() bool { return c.dx != nil }

func (c *Capturer) drawCursor() {
	ci := cursorInfo{}
	ci.Size = uint32(unsafe.Sizeof(ci))
	if r, _, _ := pGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r == 0 || ci.Flags&1 == 0 || ci.Cursor == 0 {
		return
	}
	hot, ok := c.hot[ci.Cursor]
	if !ok {
		var ii iconInfo
		if r, _, _ := pGetIconInfo.Call(ci.Cursor, uintptr(unsafe.Pointer(&ii))); r != 0 {
			hot = [2]int32{int32(ii.XHot), int32(ii.YHot)}
			if ii.Mask != 0 {
				pDeleteObject.Call(ii.Mask)
			}
			if ii.Color != 0 {
				pDeleteObject.Call(ii.Color)
			}
		}
		if len(c.hot) > 128 {
			clear(c.hot)
		}
		c.hot[ci.Cursor] = hot
	}
	x, y := ci.Pos.X-hot[0]-int32(c.x), ci.Pos.Y-hot[1]-int32(c.y)
	if x >= int32(c.w) || y >= int32(c.h) || x < -64 || y < -64 {
		return // cursor em outro monitor
	}
	pDrawIconEx.Call(c.memDC, uintptr(x), uintptr(y), ci.Cursor, 0, 0, 0, 0, diNormal)
}
