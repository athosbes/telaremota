//go:build windows

// Package viewer é o visualizador nativo (janela Win32, sem navegador).
package viewer

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"telaremota/internal/clipboard"
	"telaremota/internal/winui"
	"telaremota/internal/wire"
)

// Itens adicionados ao menu do sistema (clique direito na barra de título).
const (
	cmdCAD     = 0x0100
	cmdFull    = 0x0110
	cmdQLow    = 0x0120
	cmdQMed    = 0x0130
	cmdQHigh   = 0x0140
	cmdRefresh = 0x0150
	cmdMon0    = 0x0200 // + índice<<4 (até 16 telas)
	cmdMonAll  = 0x0300
)

type Viewer struct {
	name string
	out  *wire.Writer
	hwnd uintptr

	mu     sync.RWMutex // protege o buffer de imagem, info/título e telas
	memDC  uintptr
	bmp    uintptr
	oldBmp uintptr
	bits   unsafe.Pointer
	w, h   int
	info   string
	title  string
	err    error
	mons   []monitor // telas do computador remoto
	monSel int       // tela mostrada (wire.MonitorAll = todas)

	// usados só na thread da janela
	pressed    map[uint16]bool
	buttons    int
	fullscreen bool
	savedStyle uintptr
	wp         windowPlacement
	fitW, fitH int // tamanho remoto ao qual a janela foi ajustada
	monBase    int // posição do primeiro item de tela no menu do sistema (-1 = nenhum)

	frames   atomic.Int64
	bytesIn  atomic.Int64
	clipSkip atomic.Uint32
}

var cur *Viewer

// Run abre a janela e atende a sessão até o usuário fechar ou a conexão cair.
func Run(name string, r io.Reader, w io.Writer) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	v := &Viewer{name: name, out: wire.NewWriter(w), pressed: map[uint16]bool{}, monBase: -1}
	cur = v
	if err := v.createWindow(); err != nil {
		return err
	}
	hook, _, _ := pSetWindowsHookEx.Call(whKeyboardLL, windows.NewCallback(hookProc), moduleHandle(), 0)

	v.out.Send(wire.Quality, []byte{60})
	v.out.Send(wire.Refresh)
	go v.readLoop(bufio.NewReaderSize(r, 256<<10))
	go v.statsLoop()
	go v.clipLoop()

	var m msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	if hook != 0 {
		pUnhookWindowsHook.Call(hook)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.freeBuffer()
	return v.err
}

func moduleHandle() uintptr {
	h, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	return h
}

func (v *Viewer) createWindow() error {
	inst := moduleHandle()
	cursor, _, _ := pLoadCursor.Call(0, idcArrow)
	icon, iconSm := winui.AppIcons()
	cls := utf16("TelaRemotaViewer")
	wc := wndClassEx{
		WndProc:   windows.NewCallback(wndProc),
		Instance:  inst,
		Cursor:    cursor,
		Icon:      icon,
		IconSm:    iconSm,
		ClassName: cls,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	v.title = v.name + " — TelaRemota (conectando…)"
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(utf16(v.title))),
		wsOverlappedWin|wsVisible, cwUseDefault, cwUseDefault, 1280, 760, 0, 0, inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("não foi possível criar a janela: %v", err)
	}
	v.hwnd = hwnd
	sm, _, _ := pGetSystemMenu.Call(hwnd, 0)
	add := func(id uintptr, text string) {
		if id == 0 {
			pAppendMenu.Call(sm, mfSeparator, 0, 0)
			return
		}
		pAppendMenu.Call(sm, mfString, id, uintptr(unsafe.Pointer(utf16(text))))
	}
	add(0, "")
	add(cmdCAD, "Enviar Ctrl+Alt+Del")
	add(cmdFull, "Tela cheia\tCtrl+Alt+Enter")
	add(cmdRefresh, "Atualizar imagem")
	add(0, "")
	add(cmdQLow, "Qualidade baixa (conexão lenta)")
	add(cmdQMed, "Qualidade média")
	add(cmdQHigh, "Qualidade alta")
	pSetForegroundWnd.Call(hwnd)
	return nil
}

func (v *Viewer) post(m uint32) { pPostMessage.Call(v.hwnd, uintptr(m), 0, 0) }

func (v *Viewer) quit(err error) {
	v.mu.Lock()
	if v.err == nil {
		v.err = err
	}
	v.mu.Unlock()
	v.post(wmAppQuit)
}

type monitor struct {
	x, y, w, h int
	primary    bool
}

// ---------------------------------------------------------------- rede

func (v *Viewer) readLoop(br *bufio.Reader) {
	// Os blocos de um quadro não se sobrepõem: são decodificados em paralelo
	// e o quadro só é exibido (e confirmado) quando todos terminam.
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for {
		t, p, err := wire.Read(br)
		if err != nil {
			if err == io.EOF {
				err = fmt.Errorf("o computador remoto encerrou a sessão")
			} else {
				err = fmt.Errorf("conexão perdida: %w", err)
			}
			v.quit(err)
			return
		}
		v.bytesIn.Add(int64(len(p) + 5))
		switch t {
		case wire.Size:
			wg.Wait()
			if len(p) >= 4 {
				v.resize(int(binary.BigEndian.Uint16(p)), int(binary.BigEndian.Uint16(p[2:])))
				v.post(wmAppFit)
			}
		case wire.Rect:
			if len(p) > 8 {
				sem <- struct{}{}
				wg.Add(1)
				go func() {
					defer func() { <-sem; wg.Done() }()
					v.drawRect(p)
				}()
			}
		case wire.FrameEnd:
			wg.Wait()
			v.post(wmAppRedraw)
			v.out.Send(wire.Ack, p)
			v.frames.Add(1)
		case wire.ClipH:
			if clipboard.Set(string(p)) == nil {
				v.clipSkip.Store(clipboard.Seq())
			}
		case wire.Monitors:
			v.setMonitors(p)
		case wire.Info:
			v.mu.Lock()
			v.info = string(p)
			v.mu.Unlock()
			v.post(wmAppTitle)
		}
	}
}

func (v *Viewer) statsLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		f, b := v.frames.Swap(0), v.bytesIn.Swap(0)
		v.mu.Lock()
		s := fmt.Sprintf("%s — TelaRemota · %d qps · %.1f Mb/s", v.name, f, float64(b)*8/1e6)
		if v.w > 0 {
			s += fmt.Sprintf(" · %d×%d", v.w, v.h)
		}
		if n := len(v.mons); n > 1 {
			if v.monSel == wire.MonitorAll {
				s += " · todas as telas"
			} else {
				s += fmt.Sprintf(" · tela %d de %d", v.monSel+1, n)
			}
			s += " (Ctrl+Alt+←/→)"
		}
		if v.info != "" {
			s += " · " + v.info
		}
		v.title = s
		v.mu.Unlock()
		v.post(wmAppTitle)
	}
}

// clipLoop envia ao computador remoto o texto copiado aqui.
func (v *Viewer) clipLoop() {
	last := clipboard.Seq()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		n := clipboard.Seq()
		if n == last {
			continue
		}
		last = n
		if n == v.clipSkip.Load() {
			continue
		}
		if text, err := clipboard.Get(); err == nil && text != "" {
			v.out.Send(wire.ClipV, []byte(text))
		}
	}
}

// ---------------------------------------------------------------- imagem

func (v *Viewer) freeBuffer() {
	if v.memDC != 0 {
		pSelectObject.Call(v.memDC, v.oldBmp)
		pDeleteObject.Call(v.bmp)
		pDeleteDC.Call(v.memDC)
	}
	v.memDC, v.bmp, v.oldBmp, v.bits = 0, 0, 0, nil
}

func (v *Viewer) resize(w, h int) {
	if w <= 0 || h <= 0 {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if w == v.w && h == v.h && v.memDC != 0 {
		return
	}
	v.freeBuffer()
	dc, _, _ := pCreateCompatibleDC.Call(0)
	bi := bitmapInfoHeader{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		pDeleteDC.Call(dc)
		return
	}
	old, _, _ := pSelectObject.Call(dc, bmp)
	v.memDC, v.bmp, v.oldBmp, v.bits, v.w, v.h = dc, bmp, old, bits, w, h
}

func (v *Viewer) drawRect(p []byte) {
	x := int(binary.BigEndian.Uint16(p[0:]))
	y := int(binary.BigEndian.Uint16(p[2:]))
	img, err := jpeg.Decode(bytes.NewReader(p[8:]))
	if err != nil {
		return
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.bits == nil {
		return
	}
	dst := unsafe.Slice((*byte)(v.bits), v.w*v.h*4)
	blit(dst, v.w, v.h, img, x, y)
}

// blit copia a imagem decodificada (YCbCr) para o buffer BGRA.
func blit(dst []byte, dw, dh int, img image.Image, x0, y0 int) {
	b := img.Bounds()
	w, h := min(b.Dx(), dw-x0), min(b.Dy(), dh-y0)
	if w <= 0 || h <= 0 || x0 < 0 || y0 < 0 {
		return
	}
	if m, ok := img.(*image.YCbCr); ok {
		hs, known := chromaShift(m.SubsampleRatio)
		known = known && b.Min.X == 0
		for y := 0; y < h; y++ {
			row := dst[((y0+y)*dw+x0)*4:][:w*4]
			if known {
				// caminho rápido: sem chamadas YOffset/COffset por pixel
				yr := m.Y[m.YOffset(b.Min.X, b.Min.Y+y):][:w]
				c0 := m.COffset(b.Min.X, b.Min.Y+y)
				cb, cr := m.Cb[c0:], m.Cr[c0:]
				for x := range w {
					r, g, bb := color.YCbCrToRGB(yr[x], cb[x>>hs], cr[x>>hs])
					o := x * 4
					row[o], row[o+1], row[o+2], row[o+3] = bb, g, r, 255
				}
				continue
			}
			for x := 0; x < w; x++ {
				yi := m.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := m.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bb := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
				o := x * 4
				row[o], row[o+1], row[o+2], row[o+3] = bb, g, r, 255
			}
		}
		return
	}
	for y := 0; y < h; y++ {
		row := dst[((y0+y)*dw+x0)*4:]
		for x := 0; x < w; x++ {
			r, g, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			o := x * 4
			row[o], row[o+1], row[o+2], row[o+3] = byte(bb>>8), byte(g>>8), byte(r>>8), 255
		}
	}
}

// chromaShift diz quantos bits deslocar x para achar a amostra de cor.
func chromaShift(r image.YCbCrSubsampleRatio) (int, bool) {
	switch r {
	case image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio440:
		return 0, true
	case image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420:
		return 1, true
	}
	return 0, false
}

// dest calcula onde a imagem remota fica na área cliente (mantendo proporção).
func dest(cw, ch, w, h int) (dx, dy, dw, dh int) {
	if w == 0 || h == 0 || cw == 0 || ch == 0 {
		return 0, 0, 0, 0
	}
	s := min(float64(cw)/float64(w), float64(ch)/float64(h))
	dw, dh = int(float64(w)*s+0.5), int(float64(h)*s+0.5)
	return (cw - dw) / 2, (ch - dh) / 2, dw, dh
}

func (v *Viewer) clientSize() (int, int) {
	var r rect
	pGetClientRect.Call(v.hwnd, uintptr(unsafe.Pointer(&r)))
	return int(r.R), int(r.B)
}

func (v *Viewer) paint() {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(v.hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(v.hwnd, uintptr(unsafe.Pointer(&ps)))
	cw, ch := v.clientSize()
	black, _, _ := pGetStockObject.Call(blackBrush)
	fill := func(l, t, r, b int) {
		if r > l && b > t {
			rc := rect{int32(l), int32(t), int32(r), int32(b)}
			pFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), black)
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.memDC == 0 {
		fill(0, 0, cw, ch)
		return
	}
	dx, dy, dw, dh := dest(cw, ch, v.w, v.h)
	if dw == v.w && dh == v.h {
		pBitBlt.Call(hdc, uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh), v.memDC, 0, 0, srcCopy)
	} else {
		pSetStretchBltMode.Call(hdc, halftone)
		pSetBrushOrgEx.Call(hdc, 0, 0, 0)
		pStretchBlt.Call(hdc, uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh), v.memDC, 0, 0, uintptr(v.w), uintptr(v.h), srcCopy)
	}
	fill(0, 0, dx, ch)
	fill(dx+dw, 0, cw, ch)
	fill(dx, 0, dx+dw, dy)
	fill(dx, dy+dh, dx+dw, ch)
}

// fitWindow ajusta a janela ao tamanho da tela remota quando ele muda
// (primeira imagem ou troca de tela), exceto em tela cheia/maximizada.
func (v *Viewer) fitWindow() {
	v.mu.RLock()
	w, h := v.w, v.h
	v.mu.RUnlock()
	zoomed, _, _ := pIsZoomed.Call(v.hwnd)
	if w == 0 || (w == v.fitW && h == v.fitH) || v.fullscreen || zoomed != 0 {
		pInvalidateRect.Call(v.hwnd, 0, 0)
		return
	}
	v.fitW, v.fitH = w, h
	var work rect
	pSystemParamsInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0)
	ww, wh := float64(work.R-work.L)*0.94, float64(work.B-work.T)*0.94
	s := min(1, ww/float64(w), wh/float64(h))
	r := rect{0, 0, int32(float64(w) * s), int32(float64(h) * s)}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), wsOverlappedWin, 0, 0)
	fw, fh := r.R-r.L, r.B-r.T
	x := work.L + (work.R-work.L-fw)/2
	y := work.T + (work.B-work.T-fh)/2
	pSetWindowPos.Call(v.hwnd, 0, uintptr(x), uintptr(y), uintptr(fw), uintptr(fh), swpNoZOrder)
	pInvalidateRect.Call(v.hwnd, 0, 0)
}

// setMonitors recebe a lista de telas remotas (chamado na rede).
func (v *Viewer) setMonitors(p []byte) {
	if len(p) < 2 {
		return
	}
	n, sel := int(p[0]), int(p[1])
	var mons []monitor
	for i := 0; i < n && len(p) >= 2+(i+1)*9; i++ {
		b := p[2+i*9:]
		i16 := func(o int) int { return int(int16(binary.BigEndian.Uint16(b[o:]))) }
		mons = append(mons, monitor{i16(0), i16(2), i16(4) & 0xFFFF, i16(6) & 0xFFFF, b[8]&1 != 0})
	}
	v.mu.Lock()
	v.mons, v.monSel = mons, sel
	v.mu.Unlock()
	v.post(wmAppMonitors)
}

// updateMonitorMenu refaz os itens de tela no menu do sistema.
func (v *Viewer) updateMonitorMenu() {
	sm, _, _ := pGetSystemMenu.Call(v.hwnd, 0)
	if v.monBase >= 0 { // os itens de tela ficam sempre no fim do menu
		for {
			n, _, _ := pGetMenuItemCount.Call(sm)
			if int(int32(n)) <= v.monBase {
				break
			}
			pDeleteMenu.Call(sm, uintptr(v.monBase), mfByPosition)
		}
		v.monBase = -1
	}
	v.mu.RLock()
	mons, sel := v.mons, v.monSel
	v.mu.RUnlock()
	if len(mons) < 2 {
		return
	}
	n, _, _ := pGetMenuItemCount.Call(sm)
	v.monBase = int(int32(n))
	add := func(id uintptr, checked bool, text string) {
		f := uintptr(mfString)
		if id == 0 {
			f = mfSeparator
		} else if checked {
			f |= mfChecked
		}
		pAppendMenu.Call(sm, f, id, uintptr(unsafe.Pointer(utf16(text))))
	}
	add(0, false, "")
	for i, m := range mons[:min(len(mons), 16)] {
		t := fmt.Sprintf("Tela %d (%d×%d)", i+1, m.w, m.h)
		if m.primary {
			t += " — principal"
		}
		if i == 0 {
			t += "	Ctrl+Alt+←/→"
		}
		add(cmdMon0+uintptr(i)<<4, i == sel, t)
	}
	add(cmdMonAll, sel == wire.MonitorAll, "Todas as telas")
}

// selectMonitor pede ao computador remoto a tela i (wire.MonitorAll = todas).
func (v *Viewer) selectMonitor(i int) {
	v.out.Send(wire.Monitor, []byte{byte(i)})
}

// cycleMonitor passa para a tela seguinte (d=1) ou anterior (d=-1); a última
// posição do ciclo é "todas as telas".
func (v *Viewer) cycleMonitor(d int) {
	v.mu.RLock()
	n, sel := len(v.mons), v.monSel
	v.mu.RUnlock()
	if n < 2 {
		return
	}
	n = min(n, 16)
	if sel == wire.MonitorAll {
		sel = n
	}
	sel = (sel + d + n + 1) % (n + 1)
	if sel == n {
		sel = wire.MonitorAll
	}
	v.selectMonitor(sel)
}

func (v *Viewer) toggleFullscreen() {
	if !v.fullscreen {
		v.savedStyle, _, _ = pGetWindowLongPtr.Call(v.hwnd, gwlStyle)
		v.wp.Length = uint32(unsafe.Sizeof(v.wp))
		pGetWindowPlacement.Call(v.hwnd, uintptr(unsafe.Pointer(&v.wp)))
		mon, _, _ := pMonitorFromWindow.Call(v.hwnd, 2)
		mi := monitorInfo{}
		mi.Size = uint32(unsafe.Sizeof(mi))
		pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi)))
		pSetWindowLongPtr.Call(v.hwnd, gwlStyle, v.savedStyle&^wsOverlappedWin)
		m := mi.Monitor
		pSetWindowPos.Call(v.hwnd, 0, uintptr(m.L), uintptr(m.T), uintptr(m.R-m.L), uintptr(m.B-m.T), swpNoOwnerZ|swpFrameChanged)
		v.fullscreen = true
	} else {
		pSetWindowLongPtr.Call(v.hwnd, gwlStyle, v.savedStyle)
		pSetWindowPlacement.Call(v.hwnd, uintptr(unsafe.Pointer(&v.wp)))
		pSetWindowPos.Call(v.hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpNoOwnerZ|swpFrameChanged)
		v.fullscreen = false
	}
	pInvalidateRect.Call(v.hwnd, 0, 0)
}

// ---------------------------------------------------------------- entrada

func (v *Viewer) toRemote(cx, cy int) (int, int, bool) {
	cw, ch := v.clientSize()
	v.mu.Lock()
	w, h := v.w, v.h
	v.mu.Unlock()
	dx, dy, dw, dh := dest(cw, ch, w, h)
	if dw == 0 {
		return 0, 0, false
	}
	x := (cx - dx) * w / dw
	y := (cy - dy) * h / dh
	return max(0, min(w-1, x)), max(0, min(h-1, y)), true
}

func (v *Viewer) mouseMove(lp uintptr) {
	if x, y, ok := v.toRemote(loword(lp), hiword(lp)); ok {
		v.out.Send(wire.Move, wire.U16(x), wire.U16(y))
	}
}

func (v *Viewer) button(b int, down bool, lp uintptr) {
	v.mouseMove(lp)
	d := byte(0)
	if down {
		d = 1
		if v.buttons == 0 {
			pSetCapture.Call(v.hwnd)
		}
		v.buttons++
	} else if v.buttons > 0 {
		v.buttons--
		if v.buttons == 0 {
			pReleaseCapture.Call()
		}
	}
	v.out.Send(wire.Button, []byte{byte(b), d})
}

func (v *Viewer) key(sc uint16, down bool) {
	if down {
		v.pressed[sc] = true
	} else {
		if !v.pressed[sc] {
			return
		}
		delete(v.pressed, sc)
	}
	d := byte(0)
	if down {
		d = 1
	}
	v.out.Send(wire.Key, wire.U16(int(sc)), []byte{d})
}

func (v *Viewer) releaseAll() {
	for sc := range v.pressed {
		v.key(sc, false)
	}
	for v.buttons > 0 { // solta todos os botões conhecidos
		v.buttons = 0
		for _, b := range []byte{0, 1, 2} {
			v.out.Send(wire.Button, []byte{b, 0})
		}
		pReleaseCapture.Call()
	}
}

func (v *Viewer) keyMsg(m uint32, wp, lp uintptr) {
	down := m == wmKeyDown || m == wmSysKeyDown
	vk := int(wp)
	if down && keyDown(0x11) && keyDown(0x12) {
		switch vk {
		case 0x0D: // Ctrl+Alt+Enter: tela cheia local
			v.toggleFullscreen()
			return
		case 0x25, 0x27: // Ctrl+Alt+←/→: tela remota anterior/seguinte
			v.cycleMonitor(vk - 0x26)
			return
		}
	}
	var sc uint16
	if vk == 0x13 {
		sc = 0xE11D // Pause
	} else {
		sc = uint16((lp >> 16) & 0xFF)
		if sc == 0 {
			r, _, _ := pMapVirtualKey.Call(uintptr(vk), 0)
			sc = uint16(r)
		}
		if lp&(1<<24) != 0 {
			sc |= 0xE000
		}
	}
	if sc != 0 {
		v.key(sc, down)
	}
}

func (v *Viewer) sysCommand(id uintptr) bool {
	switch id {
	case cmdCAD:
		v.out.Send(wire.CAD)
	case cmdFull:
		v.toggleFullscreen()
	case cmdRefresh:
		v.out.Send(wire.Refresh)
	case cmdQLow:
		v.out.Send(wire.Quality, []byte{35})
	case cmdQMed:
		v.out.Send(wire.Quality, []byte{60})
	case cmdQHigh:
		v.out.Send(wire.Quality, []byte{85})
	case cmdMonAll:
		v.selectMonitor(wire.MonitorAll)
	default:
		if id >= cmdMon0 && id < cmdMon0+16<<4 {
			v.selectMonitor(int(id-cmdMon0) >> 4)
			return true
		}
		return false
	}
	return true
}

func wndProc(hwnd, m, wp, lp uintptr) uintptr {
	v := cur
	if v == nil || (v.hwnd != 0 && hwnd != v.hwnd) {
		r, _, _ := pDefWindowProc.Call(hwnd, m, wp, lp)
		return r
	}
	switch uint32(m) {
	case wmPaint:
		v.paint()
		return 0
	case wmEraseBkgnd:
		return 1
	case wmSize, wmAppRedraw:
		pInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmAppFit:
		v.fitWindow()
		return 0
	case wmAppMonitors:
		v.updateMonitorMenu()
		return 0
	case wmAppTitle:
		v.mu.Lock()
		t := v.title
		v.mu.Unlock()
		pSetWindowText.Call(hwnd, uintptr(unsafe.Pointer(utf16(t))))
		return 0
	case wmAppQuit:
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	case wmKillFocus:
		v.releaseAll()
	case wmSysCommand:
		if v.sysCommand(wp & 0xFFF0) {
			return 0
		}
	case wmMouseMove:
		v.mouseMove(lp)
		return 0
	case wmLButtonDown, wmLButtonUp:
		v.button(0, m == wmLButtonDown, lp)
		return 0
	case wmMButtonDown, wmMButtonUp:
		v.button(1, m == wmMButtonDown, lp)
		return 0
	case wmRButtonDown, wmRButtonUp:
		v.button(2, m == wmRButtonDown, lp)
		return 0
	case wmXButtonDown, wmXButtonUp:
		b := 3
		if hiword(wp) == 2 {
			b = 4
		}
		v.button(b, m == wmXButtonDown, lp)
		return 1
	case wmMouseWheel:
		v.out.Send(wire.Wheel, wire.U16(hiword(wp)), wire.U16(0))
		return 0
	case wmMouseHWheel:
		v.out.Send(wire.Wheel, wire.U16(0), wire.U16(hiword(wp)))
		return 0
	case wmKeyDown, wmKeyUp, wmSysKeyDown, wmSysKeyUp:
		v.keyMsg(uint32(m), wp, lp)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, m, wp, lp)
	return r
}

// hookProc captura teclas que o Windows não entrega à janela (Win, Alt+Tab,
// Alt+Esc, Ctrl+Esc, PrintScreen) enquanto o visualizador está em primeiro plano.
func hookProc(code, wp, lp uintptr) uintptr {
	v := cur
	if int32(code) == 0 && v != nil && v.hwnd != 0 {
		if fg, _, _ := pGetForegroundWnd.Call(); fg == v.hwnd {
			k := (*kbdLL)(ptr(lp))
			if k.Flags&0x10 == 0 { // ignora eventos injetados
				vk := k.VkCode
				alt := k.Flags&0x20 != 0
				ctrl := asyncDown(0x11)
				if vk == 0x5B || vk == 0x5C || vk == 0x2C ||
					(alt && (vk == 0x09 || vk == 0x1B)) || (ctrl && vk == 0x1B) {
					sc := uint16(k.ScanCode)
					if k.Flags&1 != 0 {
						sc |= 0xE000
					}
					if vk == 0x2C {
						sc = 0xE037
					}
					v.key(sc, k.Flags&0x80 == 0)
					return 1
				}
			}
		}
	}
	r, _, _ := pCallNextHookEx.Call(0, code, wp, lp)
	return r
}
