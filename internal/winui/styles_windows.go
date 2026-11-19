//go:build windows

package winui

import (
	_ "embed"
	"os"
	"path/filepath"
	"sync"
	"unsafe"
)

//go:embed icon.png
var iconPNG []byte

var (
	pCreateActCtx      = kernel32.NewProc("CreateActCtxW")
	pActivateActCtx    = kernel32.NewProc("ActivateActCtx")
	pCreateIconFromRes = user32.NewProc("CreateIconFromResourceEx")
	pLoadImage         = user32.NewProc("LoadImageW")
	pGetSystemMetrics  = user32.NewProc("GetSystemMetrics")
)

const ccManifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <dependency><dependentAssembly>
    <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0"
      processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
  </dependentAssembly></dependency>
</assembly>`

type actCtx struct {
	Size     uint32
	Flags    uint32
	Source   *uint16
	ProcArch uint16
	LangID   uint16
	AsmDir   *uint16
	ResName  *uint16
	AppName  *uint16
	Module   uintptr
}

var stylesOnce sync.Once

// EnableVisualStyles ativa os controles comuns v6 (visual moderno) na thread
// atual, sem precisar de manifesto embutido no executável.
func EnableVisualStyles() {
	stylesOnce.Do(func() {
		path := filepath.Join(os.TempDir(), "telaremota-cc6.manifest")
		if os.WriteFile(path, []byte(ccManifest), 0o644) != nil {
			return
		}
		ctx := actCtx{Source: u16(path)}
		ctx.Size = uint32(unsafe.Sizeof(ctx))
		h, _, _ := pCreateActCtx.Call(uintptr(unsafe.Pointer(&ctx)))
		if h == 0 || h == ^uintptr(0) {
			return
		}
		var cookie uintptr
		pActivateActCtx.Call(h, uintptr(unsafe.Pointer(&cookie)))
	})
}

var (
	iconOnce       sync.Once
	iconH, iconSmH uintptr
)

// AppIcons devolve o ícone grande (barra de tarefas, Alt+Tab) e o pequeno
// (barra de título) do aplicativo. Usa o .ico embutido no executável
// (cmd/telaremota/telaremota.ico), que tem cada tamanho desenhado à parte; sem
// ele (go run, outra arquitetura), cai para o PNG embutido, reduzido pelo Windows.
func AppIcons() (big, small uintptr) {
	iconOnce.Do(func() {
		iconH = resourceIcon(11, 12)   // SM_CXICON, SM_CYICON
		iconSmH = resourceIcon(49, 50) // SM_CXSMICON, SM_CYSMICON
		if iconH == 0 || iconSmH == 0 {
			h, _, _ := pCreateIconFromRes.Call(uintptr(unsafe.Pointer(&iconPNG[0])), uintptr(len(iconPNG)), 1, 0x00030000, 0, 0, 0)
			if h == 0 {
				h, _, _ = pLoadIcon.Call(0, 32512)
			}
			iconH, iconSmH = h, h
		}
	})
	return iconH, iconSmH
}

// resourceIcon carrega o ícone 1 do executável no tamanho da métrica dada.
func resourceIcon(mx, my uintptr) uintptr {
	inst, _, _ := pGetModuleHandle.Call(0)
	cx, _, _ := pGetSystemMetrics.Call(mx)
	cy, _, _ := pGetSystemMetrics.Call(my)
	h, _, _ := pLoadImage.Call(inst, 1, 1, cx, cy, 0) // ícone nº 1, IMAGE_ICON
	return h
}
