//go:build windows

package screen

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Captura por DXGI Desktop Duplication: a GPU entrega a imagem já composta,
// muito mais rápido que BitBlt (que lê de volta a área de trabalho do DWM), e
// avisa quando nada mudou, poupando a comparação de quadros.

var (
	dxgiDLL  = windows.NewLazySystemDLL("dxgi.dll")
	d3d11DLL = windows.NewLazySystemDLL("d3d11.dll")

	pCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
	pD3D11CreateDevice  = d3d11DLL.NewProc("D3D11CreateDevice")

	iidFactory1  = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	iidOutput1   = windows.GUID{Data1: 0x00cddea8, Data2: 0x939b, Data3: 0x4b83, Data4: [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidTexture2D = windows.GUID{Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89, Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

const (
	dxgiErrNotFound      = 0x887A0002
	dxgiErrWaitTimeout   = 0x887A0027
	d3dDriverTypeUnknown = 0
	d3dSDKVersion        = 7
	fmtB8G8R8A8          = 87
	usageStaging         = 3
	cpuAccessRead        = 0x20000
	mapRead              = 1
)

// Índices nas tabelas de métodos (vtables) das interfaces COM.
const (
	vtQueryInterface = 0
	vtRelease        = 2
	vtEnumAdapters1  = 12 // IDXGIFactory1
	vtEnumOutputs    = 7  // IDXGIAdapter
	vtOutputGetDesc  = 7  // IDXGIOutput
	vtDuplicate      = 22 // IDXGIOutput1::DuplicateOutput
	vtAcquireNext    = 8  // IDXGIOutputDuplication
	vtReleaseFrame   = 14
	vtCreateTex2D    = 5  // ID3D11Device
	vtMap            = 14 // ID3D11DeviceContext
	vtUnmap          = 15
	vtCopyResource   = 47
)

func comCall(obj uintptr, idx int, args ...uintptr) uintptr {
	fn := load(load(obj) + uintptr(idx)*unsafe.Sizeof(uintptr(0)))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

// load lê um ponteiro na memória do COM (endereço vindo da API).
func load(addr uintptr) uintptr { return *(*uintptr)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))) }

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, vtRelease)
	}
}

type outputDesc struct {
	DeviceName [32]uint16
	Coords     rect
	Attached   int32
	Rotation   uint32
	Monitor    uintptr
}

type frameInfo struct {
	LastPresent, LastMouse int64
	Accumulated            uint32
	Coalesced, Masked      int32
	PtrX, PtrY, PtrVisible int32
	MetaSize, ShapeSize    uint32
}

type tex2DDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccess, MiscFlags      uint32
}

type mapped struct {
	Data       uintptr
	RowPitch   uint32
	DepthPitch uint32
}

// dupl duplica um monitor (uma saída de vídeo) via DXGI.
type dupl struct {
	x, y, w, h    int
	dev, ctx, dup uintptr
	staging       uintptr
	held          bool // quadro adquirido e ainda não liberado
}

var errNoChange = errors.New("sem mudança")

// newDupl abre a duplicação do monitor cujo retângulo é (x,y,w,h).
func newDupl(x, y, w, h int) (*dupl, error) {
	if err := pCreateDXGIFactory1.Find(); err != nil {
		return nil, err
	}
	var factory uintptr
	if r, _, _ := pCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidFactory1)), uintptr(unsafe.Pointer(&factory))); r != 0 {
		return nil, fmt.Errorf("CreateDXGIFactory1: %#x", uint32(r))
	}
	defer comRelease(factory)
	for ai := 0; ; ai++ {
		var adapter uintptr
		if r := comCall(factory, vtEnumAdapters1, uintptr(ai), uintptr(unsafe.Pointer(&adapter))); uint32(r) == dxgiErrNotFound {
			break
		} else if r != 0 {
			return nil, fmt.Errorf("EnumAdapters1: %#x", uint32(r))
		}
		for oi := 0; ; oi++ {
			var out uintptr
			if r := comCall(adapter, vtEnumOutputs, uintptr(oi), uintptr(unsafe.Pointer(&out))); r != 0 {
				break
			}
			var d outputDesc
			comCall(out, vtOutputGetDesc, uintptr(unsafe.Pointer(&d)))
			c := d.Coords
			if d.Attached != 0 && int(c.L) == x && int(c.T) == y && int(c.R-c.L) == w && int(c.B-c.T) == h {
				if d.Rotation > 1 { // girado: a imagem viria em outra orientação
					comRelease(out)
					comRelease(adapter)
					return nil, errors.New("monitor girado")
				}
				dp, err := openDupl(adapter, out, x, y, w, h)
				comRelease(out)
				comRelease(adapter)
				return dp, err
			}
			comRelease(out)
		}
		comRelease(adapter)
	}
	return nil, errors.New("saída DXGI não encontrada")
}

func openDupl(adapter, out uintptr, x, y, w, h int) (*dupl, error) {
	d := &dupl{x: x, y: y, w: w, h: h}
	if r, _, _ := pD3D11CreateDevice.Call(adapter, d3dDriverTypeUnknown, 0, 0, 0, 0, d3dSDKVersion,
		uintptr(unsafe.Pointer(&d.dev)), 0, uintptr(unsafe.Pointer(&d.ctx))); r != 0 {
		return nil, fmt.Errorf("D3D11CreateDevice: %#x", uint32(r))
	}
	var out1 uintptr
	if r := comCall(out, vtQueryInterface, uintptr(unsafe.Pointer(&iidOutput1)), uintptr(unsafe.Pointer(&out1))); r != 0 {
		d.close()
		return nil, fmt.Errorf("IDXGIOutput1: %#x", uint32(r))
	}
	r := comCall(out1, vtDuplicate, d.dev, uintptr(unsafe.Pointer(&d.dup)))
	comRelease(out1)
	if r != 0 {
		d.close()
		return nil, fmt.Errorf("DuplicateOutput: %#x", uint32(r))
	}
	td := tex2DDesc{Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1, Format: fmtB8G8R8A8,
		SampleCount: 1, Usage: usageStaging, CPUAccess: cpuAccessRead}
	if r := comCall(d.dev, vtCreateTex2D, uintptr(unsafe.Pointer(&td)), 0, uintptr(unsafe.Pointer(&d.staging))); r != 0 {
		d.close()
		return nil, fmt.Errorf("CreateTexture2D: %#x", uint32(r))
	}
	return d, nil
}

func (d *dupl) close() {
	if d.held {
		comCall(d.dup, vtReleaseFrame)
		d.held = false
	}
	comRelease(d.staging)
	comRelease(d.dup)
	comRelease(d.ctx)
	comRelease(d.dev)
	d.staging, d.dup, d.ctx, d.dev = 0, 0, 0, 0
}

// frame espera até timeoutMs por um quadro novo e copia a imagem para dst
// (BGRA top-down, linhas de stride bytes, começando no canto do monitor). Devolve errNoChange se a tela não mudou; nesse caso
// dst não é alterado. Outros erros (ex.: troca de área de trabalho, modo de
// vídeo) exigem recriar a duplicação.
func (d *dupl) frame(dst []byte, stride, timeoutMs int) error {
	if d.held {
		comCall(d.dup, vtReleaseFrame)
		d.held = false
	}
	var fi frameInfo
	var res uintptr
	r := comCall(d.dup, vtAcquireNext, uintptr(timeoutMs), uintptr(unsafe.Pointer(&fi)), uintptr(unsafe.Pointer(&res)))
	if uint32(r) == dxgiErrWaitTimeout {
		return errNoChange
	}
	if r != 0 {
		return fmt.Errorf("AcquireNextFrame: %#x", uint32(r))
	}
	d.held = true
	if fi.LastPresent == 0 {
		// Só o ponteiro do mouse mudou (é desenhado à parte), ou é o primeiro
		// quadro, que vem vazio até a área de trabalho ser apresentada de novo.
		comRelease(res)
		return errNoChange
	}
	var tex uintptr
	r = comCall(res, vtQueryInterface, uintptr(unsafe.Pointer(&iidTexture2D)), uintptr(unsafe.Pointer(&tex)))
	comRelease(res)
	if r != 0 {
		return fmt.Errorf("ID3D11Texture2D: %#x", uint32(r))
	}
	comCall(d.ctx, vtCopyResource, d.staging, tex)
	comRelease(tex)
	var m mapped
	if r := comCall(d.ctx, vtMap, d.staging, 0, mapRead, 0, uintptr(unsafe.Pointer(&m))); r != 0 {
		return fmt.Errorf("Map: %#x", uint32(r))
	}
	row := d.w * 4
	src := unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&m.Data))), int(m.RowPitch)*(d.h-1)+row)
	if int(m.RowPitch) == row && stride == row {
		copy(dst, src[:row*d.h])
	} else {
		for y := 0; y < d.h; y++ {
			copy(dst[y*stride:y*stride+row], src[y*int(m.RowPitch):])
		}
	}
	comCall(d.ctx, vtUnmap, d.staging, 0)
	return nil
}
