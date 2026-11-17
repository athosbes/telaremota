//go:build windows

// Package winui é um conjunto mínimo de controles Win32 nativos (sem
// dependências) para o painel de configuração.
package winui

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassEx    = user32.NewProc("RegisterClassExW")
	pCreateWindowEx     = user32.NewProc("CreateWindowExW")
	pDefWindowProc      = user32.NewProc("DefWindowProcW")
	pDestroyWindow      = user32.NewProc("DestroyWindow")
	pGetMessage         = user32.NewProc("GetMessageW")
	pTranslateMessage   = user32.NewProc("TranslateMessage")
	pDispatchMessage    = user32.NewProc("DispatchMessageW")
	pIsDialogMessage    = user32.NewProc("IsDialogMessageW")
	pPostMessage        = user32.NewProc("PostMessageW")
	pSendMessage        = user32.NewProc("SendMessageW")
	pPostQuitMessage    = user32.NewProc("PostQuitMessage")
	pSetWindowText      = user32.NewProc("SetWindowTextW")
	pGetWindowText      = user32.NewProc("GetWindowTextW")
	pGetWindowTextLen   = user32.NewProc("GetWindowTextLengthW")
	pShowWindow         = user32.NewProc("ShowWindow")
	pEnableWindow       = user32.NewProc("EnableWindow")
	pLoadCursor         = user32.NewProc("LoadCursorW")
	pLoadIcon           = user32.NewProc("LoadIconW")
	pMessageBox         = user32.NewProc("MessageBoxW")
	pGetSysColorBrush   = user32.NewProc("GetSysColorBrush")
	pGetSysColor        = user32.NewProc("GetSysColor")
	pAdjustWindowRectEx = user32.NewProc("AdjustWindowRectEx")
	pSetWindowPos       = user32.NewProc("SetWindowPos")
	pSetTimer           = user32.NewProc("SetTimer")
	pGetDpiForSystem    = user32.NewProc("GetDpiForSystem")
	pSetForegroundWnd   = user32.NewProc("SetForegroundWindow")
	pSetFocus           = user32.NewProc("SetFocus")
	pSystemParamsInfo   = user32.NewProc("SystemParametersInfoW")
	pCreateFont         = gdi32.NewProc("CreateFontW")
	pSetBkColor         = gdi32.NewProc("SetBkColor")
	pSetTextColor       = gdi32.NewProc("SetTextColor")
	pCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	pCreateCompatDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatBmp    = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
	pGetPixel           = gdi32.NewProc("GetPixel")
	pGetDC              = user32.NewProc("GetDC")
	pReleaseDC          = user32.NewProc("ReleaseDC")
	pGetClientRect      = user32.NewProc("GetClientRect")
	pInitCommonControls = comctl32.NewProc("InitCommonControlsEx")
	pGetModuleHandle    = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsChild         = 0x40000000
	wsVisible       = 0x10000000
	wsTabStop       = 0x00010000
	wsGroup         = 0x00020000
	wsVScroll       = 0x00200000
	wsClipSiblings  = 0x04000000
	wsCaption       = 0x00C00000
	wsSysMenu       = 0x00080000
	wsMinimizeBox   = 0x00020000
	wsExClientEdge  = 0x00000200
	wsExControlPar  = 0x00010000
	esAutoHScroll   = 0x0080
	esNumber        = 0x2000
	esReadOnly      = 0x0800
	bsDefPush       = 0x0001
	bsAutoCheck     = 0x0003
	bsGroupBox      = 0x0007
	ssNoPrefix      = 0x0080
	ssCenter        = 0x0001
	lbsNotify       = 0x0001
	lbsNoIntegral   = 0x0100
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmSetFont       = 0x0030
	wmNotify        = 0x004E
	wmCommand       = 0x0111
	wmTimer         = 0x0113
	wmCtlColorEdit  = 0x0133
	wmCtlColorBtn   = 0x0135
	wmCtlColorStat  = 0x0138
	wmApp           = 0x8000
	bmGetCheck      = 0x00F0
	bmSetCheck      = 0x00F1
	lbAddString     = 0x0180
	lbResetContent  = 0x0184
	lbSetCurSel     = 0x0186
	lbGetCurSel     = 0x0188
	lbGetText       = 0x0189
	lbGetTextLen    = 0x018A
	lbGetCount      = 0x018B
	tcmInsertItem   = 0x133E
	tcmGetCurSel    = 0x130B
	colorWindow     = 5
	colorWindowText = 8
	colorGrayText   = 17
	swHide          = 0
	swShow          = 5
	hwndBottom      = 1
	swpNoMove       = 0x0002
	swpNoSize       = 0x0001
	spiGetWorkArea  = 0x30

	// Notificações
	BNClicked   = 0
	LBNSelChang = 1
	LBNDblClk   = 2
	TCNSelChang = -551

	MBOk       = 0x00
	MBYesNo    = 0x04
	MBIconErr  = 0x10
	MBIconQ    = 0x20
	MBIconWarn = 0x30
	MBIconInfo = 0x40
	IDYes      = 6
)

type point struct{ X, Y int32 }
type rect struct{ L, T, R, B int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	_       uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type nmhdr struct {
	HwndFrom uintptr
	IDFrom   uintptr
	Code     uint32
}

type tcItem struct {
	Mask      uint32
	State     uint32
	StateMask uint32
	Text      *uint16
	TextMax   int32
	Image     int32
	LParam    uintptr
}

func u16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func ptr(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

// App é a janela principal com seus controles.
type App struct {
	Hwnd     uintptr
	scale    float64
	font     uintptr
	inst     uintptr
	nextID   int
	onCmd    map[int]func(code int)
	onNotify map[int]func(code int)
	timers   map[uintptr]func()
	qmu      sync.Mutex
	queue    []func()
	bg       uintptr
	muted    map[uintptr]bool
	tabBg    uintptr // pincel com a cor do corpo das abas
	tabCol   uintptr
	onTab    map[uintptr]bool
}

var app *App

// Ctl é um controle filho.
type Ctl struct {
	a    *App
	Hwnd uintptr
	ID   int
}

// New cria a janela principal com área cliente w×h (em pixels lógicos de 96 DPI).
func New(title string, w, h int) (*App, error) {
	EnableVisualStyles()
	icc := [2]uint32{8, 0x0000FFFF}
	pInitCommonControls.Call(uintptr(unsafe.Pointer(&icc)))
	dpi, _, _ := pGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}
	inst, _, _ := pGetModuleHandle.Call(0)
	a := &App{scale: float64(dpi) / 96, inst: inst, nextID: 100,
		onCmd: map[int]func(int){}, onNotify: map[int]func(int){}, timers: map[uintptr]func(){}, muted: map[uintptr]bool{}, onTab: map[uintptr]bool{}}
	app = a
	a.bg, _, _ = pGetSysColorBrush.Call(colorWindow)
	a.font = a.Font("Segoe UI", 9, false)

	cursor, _, _ := pLoadCursor.Call(0, 32512)
	icon := AppIcon()
	cls := u16("TelaRemotaPainel")
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: inst, Cursor: cursor, Icon: icon, IconSm: icon, Background: a.bg, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))

	style := uintptr(wsCaption | wsSysMenu | wsMinimizeBox)
	r := rect{0, 0, int32(a.S(w)), int32(a.S(h))}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), style, 0, wsExControlPar)
	var work rect
	pSystemParamsInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0)
	ww, wh := r.R-r.L, r.B-r.T
	x, y := work.L+(work.R-work.L-ww)/2, work.T+(work.B-work.T-wh)/2
	hwnd, _, err := pCreateWindowEx.Call(wsExControlPar, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16(title))),
		style, uintptr(x), uintptr(y), uintptr(ww), uintptr(wh), 0, 0, inst, 0)
	if hwnd == 0 {
		return nil, err
	}
	a.Hwnd = hwnd
	return a, nil
}

// S converte pixels lógicos para físicos.
func (a *App) S(v int) int { return int(float64(v)*a.scale + 0.5) }

// Font cria uma fonte no tamanho em pontos, ajustada ao DPI.
func (a *App) Font(face string, pt int, bold bool) uintptr {
	weight := 400
	if bold {
		weight = 600
	}
	h := -int(float64(pt)*a.scale*96/72 + 0.5)
	f, _, _ := pCreateFont.Call(uintptr(int32(h)), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(u16(face))))
	return f
}

func (a *App) child(class, text string, style, exStyle uintptr, x, y, w, h int) *Ctl {
	id := a.nextID
	a.nextID++
	hwnd, _, _ := pCreateWindowEx.Call(exStyle, uintptr(unsafe.Pointer(u16(class))), uintptr(unsafe.Pointer(u16(text))),
		wsChild|wsVisible|style, uintptr(a.S(x)), uintptr(a.S(y)), uintptr(a.S(w)), uintptr(a.S(h)), a.Hwnd, uintptr(id), a.inst, 0)
	c := &Ctl{a: a, Hwnd: hwnd, ID: id}
	pSendMessage.Call(hwnd, wmSetFont, a.font, 1)
	return c
}

func (a *App) Label(text string, x, y, w, h int) *Ctl {
	return a.child("STATIC", text, ssNoPrefix, 0, x, y, w, h)
}

// Muted é um rótulo em cinza (dicas).
func (a *App) Muted(text string, x, y, w, h int) *Ctl {
	c := a.Label(text, x, y, w, h)
	a.muted[c.Hwnd] = true
	return c
}

func (a *App) Edit(text string, x, y, w, h int, number bool) *Ctl {
	st := uintptr(wsTabStop | esAutoHScroll)
	if number {
		st |= esNumber
	}
	return a.child("EDIT", text, st, wsExClientEdge, x, y, w, h)
}

// Password é um campo de senha (caracteres ocultos).
func (a *App) Password(x, y, w, h int) *Ctl {
	return a.child("EDIT", "", wsTabStop|esAutoHScroll|0x0020, wsExClientEdge, x, y, w, h)
}

// OnCommand trata comandos sem controle próprio (1 = Enter/OK, 2 = Esc/Cancelar).
func (a *App) OnCommand(id int, f func()) {
	a.onCmd[id] = func(int) { f() }
}

// Close fecha a janela (Run retorna).
func (a *App) Close() { pDestroyWindow.Call(a.Hwnd) }

// Focus põe o foco no controle.
func (c *Ctl) Focus() { pSetFocus.Call(c.Hwnd) }

func (a *App) Button(text string, x, y, w, h int, onClick func()) *Ctl {
	c := a.child("BUTTON", text, wsTabStop, 0, x, y, w, h)
	a.onCmd[c.ID] = func(code int) {
		if code == BNClicked {
			onClick()
		}
	}
	return c
}

func (a *App) Check(text string, x, y, w, h int, onClick func()) *Ctl {
	c := a.child("BUTTON", text, wsTabStop|bsAutoCheck, 0, x, y, w, h)
	a.onCmd[c.ID] = func(code int) {
		if code == BNClicked && onClick != nil {
			onClick()
		}
	}
	return c
}

func (a *App) Group(text string, x, y, w, h int) *Ctl {
	return a.child("BUTTON", text, bsGroupBox, 0, x, y, w, h)
}

func (a *App) ListBox(x, y, w, h int, onDblClick func()) *Ctl {
	c := a.child("LISTBOX", "", wsTabStop|wsVScroll|lbsNotify|lbsNoIntegral, wsExClientEdge, x, y, w, h)
	a.onCmd[c.ID] = func(code int) {
		if code == LBNDblClk && onDblClick != nil {
			onDblClick()
		}
	}
	return c
}

// Tabs cria abas; onChange recebe o índice selecionado.
func (a *App) Tabs(x, y, w, h int, names []string, onChange func(int)) *Ctl {
	c := a.child("SysTabControl32", "", wsClipSiblings|wsTabStop, 0, x, y, w, h)
	for i, n := range names {
		it := tcItem{Mask: 1, Text: u16(n)}
		pSendMessage.Call(c.Hwnd, tcmInsertItem, uintptr(i), uintptr(unsafe.Pointer(&it)))
	}
	a.onNotify[c.ID] = func(code int) {
		if code == TCNSelChang {
			r, _, _ := pSendMessage.Call(c.Hwnd, tcmGetCurSel, 0, 0)
			onChange(int(r))
		}
	}
	return c
}

// OnTab marca o controle como posicionado sobre o corpo de uma aba
// (o fundo dele passa a acompanhar a cor da aba).
func (c *Ctl) OnTab() { c.a.onTab[c.Hwnd] = true }

// sampleTabColor desenha a aba fora da tela e lê a cor do corpo dela.
func (a *App) sampleTabColor(tab uintptr) {
	var r rect
	pGetClientRect.Call(tab, uintptr(unsafe.Pointer(&r)))
	if r.R < 20 || r.B < 20 {
		return
	}
	sdc, _, _ := pGetDC.Call(0)
	mdc, _, _ := pCreateCompatDC.Call(sdc)
	bmp, _, _ := pCreateCompatBmp.Call(sdc, uintptr(r.R), uintptr(r.B))
	old, _, _ := pSelectObject.Call(mdc, bmp)
	pSendMessage.Call(tab, 0x0318, mdc, 0x4|0x8) // WM_PRINTCLIENT, PRF_CLIENT|PRF_ERASEBKGND
	col, _, _ := pGetPixel.Call(mdc, uintptr(r.R/2), uintptr(r.B-12))
	pSelectObject.Call(mdc, old)
	pDeleteObject.Call(bmp)
	pDeleteDC.Call(mdc)
	pReleaseDC.Call(0, sdc)
	if col == 0xFFFFFFFF {
		return
	}
	a.tabCol = col
	a.tabBg, _, _ = pCreateSolidBrush.Call(col)
}

// SendToBack coloca o controle atrás dos irmãos (necessário para abas).
func (c *Ctl) SendToBack() {
	pSetWindowPos.Call(c.Hwnd, hwndBottom, 0, 0, 0, 0, swpNoMove|swpNoSize)
	c.a.sampleTabColor(c.Hwnd)
}

func (c *Ctl) SetFont(f uintptr) { pSendMessage.Call(c.Hwnd, wmSetFont, f, 1) }

func (c *Ctl) Text() string {
	n, _, _ := pGetWindowTextLen.Call(c.Hwnd)
	buf := make([]uint16, n+1)
	pGetWindowText.Call(c.Hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return windows.UTF16ToString(buf)
}

func (c *Ctl) SetText(s string) { pSetWindowText.Call(c.Hwnd, uintptr(unsafe.Pointer(u16(s)))) }

func (c *Ctl) Checked() bool {
	r, _, _ := pSendMessage.Call(c.Hwnd, bmGetCheck, 0, 0)
	return r == 1
}

func (c *Ctl) SetChecked(b bool) {
	v := uintptr(0)
	if b {
		v = 1
	}
	pSendMessage.Call(c.Hwnd, bmSetCheck, v, 0)
}

func (c *Ctl) Show(b bool) {
	v := uintptr(swHide)
	if b {
		v = swShow
	}
	pShowWindow.Call(c.Hwnd, v)
}

func (c *Ctl) Enable(b bool) {
	v := uintptr(0)
	if b {
		v = 1
	}
	pEnableWindow.Call(c.Hwnd, v)
}

// SetItems substitui os itens da lista, mantendo a seleção pelo texto.
func (c *Ctl) SetItems(items []string) {
	sel := c.Selected()
	pSendMessage.Call(c.Hwnd, lbResetContent, 0, 0)
	for i, s := range items {
		pSendMessage.Call(c.Hwnd, lbAddString, 0, uintptr(unsafe.Pointer(u16(s))))
		if s == sel {
			pSendMessage.Call(c.Hwnd, lbSetCurSel, uintptr(i), 0)
		}
	}
}

// Selected devolve o texto do item selecionado ("" se nenhum).
func (c *Ctl) Selected() string {
	i, _, _ := pSendMessage.Call(c.Hwnd, lbGetCurSel, 0, 0)
	if int32(i) < 0 {
		return ""
	}
	n, _, _ := pSendMessage.Call(c.Hwnd, lbGetTextLen, i, 0)
	if int32(n) < 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	pSendMessage.Call(c.Hwnd, lbGetText, i, uintptr(unsafe.Pointer(&buf[0])))
	return windows.UTF16ToString(buf)
}

// Do executa f na thread da interface (seguro a partir de goroutines).
func (a *App) Do(f func()) {
	a.qmu.Lock()
	a.queue = append(a.queue, f)
	a.qmu.Unlock()
	pPostMessage.Call(a.Hwnd, wmApp, 0, 0)
}

// Every chama f a cada ms milissegundos na thread da interface.
func (a *App) Every(ms int, f func()) {
	id := uintptr(len(a.timers) + 1)
	a.timers[id] = f
	pSetTimer.Call(a.Hwnd, id, uintptr(ms), 0)
}

func (a *App) Message(text, title string, flags uintptr) int {
	r, _, _ := pMessageBox.Call(a.Hwnd, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), flags)
	return int(r)
}

// MessageBox sem janela-mãe.
func MessageBox(text, title string, flags uintptr) int {
	r, _, _ := pMessageBox.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), flags)
	return int(r)
}

// Run mostra a janela e processa mensagens até ela fechar.
func (a *App) Run() {
	pShowWindow.Call(a.Hwnd, swShow)
	pSetForegroundWnd.Call(a.Hwnd)
	var m msg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if d, _, _ := pIsDialogMessage.Call(a.Hwnd, uintptr(unsafe.Pointer(&m))); d != 0 {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd, m, wp, lp uintptr) uintptr {
	a := app
	if a != nil && (a.Hwnd == 0 || hwnd == a.Hwnd) {
		switch uint32(m) {
		case wmCommand:
			if f := a.onCmd[int(wp&0xFFFF)]; f != nil {
				f(int((wp >> 16) & 0xFFFF))
				return 0
			}
		case wmNotify:
			nm := (*nmhdr)(ptr(lp))
			if f := a.onNotify[int(nm.IDFrom)]; f != nil {
				f(int(int32(nm.Code)))
				return 0
			}
		case wmCtlColorStat, wmCtlColorBtn:
			col, brush := uintptr(0), a.bg
			if a.onTab[lp] && a.tabBg != 0 {
				col, brush = a.tabCol, a.tabBg
			} else {
				col, _, _ = pGetSysColor.Call(colorWindow)
			}
			pSetBkColor.Call(wp, col)
			if a.muted[lp] {
				g, _, _ := pGetSysColor.Call(colorGrayText)
				pSetTextColor.Call(wp, g)
			}
			return brush
		case wmApp:
			a.qmu.Lock()
			q := a.queue
			a.queue = nil
			a.qmu.Unlock()
			for _, f := range q {
				f()
			}
			return 0
		case wmTimer:
			if f := a.timers[wp]; f != nil {
				f()
			}
			return 0
		case wmClose:
			pDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			pPostQuitMessage.Call(0)
			return 0
		}
	}
	r, _, _ := pDefWindowProc.Call(hwnd, m, wp, lp)
	return r
}
