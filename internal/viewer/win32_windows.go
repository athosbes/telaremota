//go:build windows

package viewer

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	pRegisterClassEx    = user32.NewProc("RegisterClassExW")
	pCreateWindowEx     = user32.NewProc("CreateWindowExW")
	pDefWindowProc      = user32.NewProc("DefWindowProcW")
	pDestroyWindow      = user32.NewProc("DestroyWindow")
	pGetMessage         = user32.NewProc("GetMessageW")
	pDispatchMessage    = user32.NewProc("DispatchMessageW")
	pPostMessage        = user32.NewProc("PostMessageW")
	pPostQuitMessage    = user32.NewProc("PostQuitMessage")
	pBeginPaint         = user32.NewProc("BeginPaint")
	pEndPaint           = user32.NewProc("EndPaint")
	pGetClientRect      = user32.NewProc("GetClientRect")
	pInvalidateRect     = user32.NewProc("InvalidateRect")
	pSetWindowText      = user32.NewProc("SetWindowTextW")
	pLoadCursor         = user32.NewProc("LoadCursorW")
	pLoadIcon           = user32.NewProc("LoadIconW")
	pSetCapture         = user32.NewProc("SetCapture")
	pReleaseCapture     = user32.NewProc("ReleaseCapture")
	pGetSystemMenu      = user32.NewProc("GetSystemMenu")
	pAppendMenu         = user32.NewProc("AppendMenuW")
	pDeleteMenu         = user32.NewProc("DeleteMenu")
	pIsZoomed           = user32.NewProc("IsZoomed")
	pGetMenuItemCount   = user32.NewProc("GetMenuItemCount")
	pSetWindowsHookEx   = user32.NewProc("SetWindowsHookExW")
	pUnhookWindowsHook  = user32.NewProc("UnhookWindowsHookEx")
	pCallNextHookEx     = user32.NewProc("CallNextHookEx")
	pGetForegroundWnd   = user32.NewProc("GetForegroundWindow")
	pGetKeyState        = user32.NewProc("GetKeyState")
	pGetAsyncKeyState   = user32.NewProc("GetAsyncKeyState")
	pMapVirtualKey      = user32.NewProc("MapVirtualKeyW")
	pGetWindowLongPtr   = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtr   = user32.NewProc("SetWindowLongPtrW")
	pSetWindowPos       = user32.NewProc("SetWindowPos")
	pMonitorFromWindow  = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfo     = user32.NewProc("GetMonitorInfoW")
	pGetWindowPlacement = user32.NewProc("GetWindowPlacement")
	pSetWindowPlacement = user32.NewProc("SetWindowPlacement")
	pShowWindow         = user32.NewProc("ShowWindow")
	pFillRect           = user32.NewProc("FillRect")
	pSystemParamsInfo   = user32.NewProc("SystemParametersInfoW")
	pAdjustWindowRectEx = user32.NewProc("AdjustWindowRectEx")
	pSetForegroundWnd   = user32.NewProc("SetForegroundWindow")

	pCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	pCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pDeleteDC           = gdi32.NewProc("DeleteDC")
	pStretchBlt         = gdi32.NewProc("StretchBlt")
	pBitBlt             = gdi32.NewProc("BitBlt")
	pSetStretchBltMode  = gdi32.NewProc("SetStretchBltMode")
	pSetBrushOrgEx      = gdi32.NewProc("SetBrushOrgEx")
	pGetStockObject     = gdi32.NewProc("GetStockObject")
)

const (
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmKillFocus     = 0x0008
	wmPaint         = 0x000F
	wmClose         = 0x0010
	wmEraseBkgnd    = 0x0014
	wmKeyDown       = 0x0100
	wmKeyUp         = 0x0101
	wmSysKeyDown    = 0x0104
	wmSysKeyUp      = 0x0105
	wmSysCommand    = 0x0112
	wmMouseMove     = 0x0200
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmRButtonDown   = 0x0204
	wmRButtonUp     = 0x0205
	wmMButtonDown   = 0x0207
	wmMButtonUp     = 0x0208
	wmMouseWheel    = 0x020A
	wmXButtonDown   = 0x020B
	wmXButtonUp     = 0x020C
	wmMouseHWheel   = 0x020E
	wmApp           = 0x8000
	wmAppRedraw     = wmApp + 1
	wmAppTitle      = wmApp + 2
	wmAppQuit       = wmApp + 3
	wmAppFit        = wmApp + 4
	wmAppMonitors   = wmApp + 5
	wsOverlappedWin = 0x00CF0000
	wsVisible       = 0x10000000
	cwUseDefault    = 0x80000000
	srcCopy         = 0x00CC0020
	halftone        = 4
	colorOnColor    = 3
	blackBrush      = 4
	idcArrow        = 32512
	idiApplication  = 32512
	whKeyboardLL    = 13
	mfString        = 0x0
	mfSeparator     = 0x800
	mfChecked       = 0x8
	mfByPosition    = 0x400
	spiGetWorkArea  = 0x30
	swpNoZOrder     = 0x0004
	swpFrameChanged = 0x0020
	swpNoOwnerZ     = 0x0200
	swpNoMove       = 0x0002
	swpNoSize       = 0x0001
	swShow          = 5
)

var gwlStyle = ^uintptr(15) // -16

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

type paintStruct struct {
	Hdc     uintptr
	Erase   int32
	Paint   rect
	Restore int32
	IncUp   int32
	_       [36]byte
}

type windowPlacement struct {
	Length  uint32
	Flags   uint32
	ShowCmd uint32
	MinPos  point
	MaxPos  point
	Normal  rect
	Device  rect
}

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

type kbdLL struct {
	VkCode   uint32
	ScanCode uint32
	Flags    uint32
	Time     uint32
	Extra    uintptr
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

func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

// ptr converte um endereço vindo da API em ponteiro (sem alerta do vet).
func ptr(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

func loword(v uintptr) int { return int(int16(v & 0xFFFF)) }
func hiword(v uintptr) int { return int(int16((v >> 16) & 0xFFFF)) }

func keyDown(vk int) bool {
	r, _, _ := pGetKeyState.Call(uintptr(vk))
	return r&0x8000 != 0
}

func asyncDown(vk int) bool {
	r, _, _ := pGetAsyncKeyState.Call(uintptr(vk))
	return r&0x8000 != 0
}
