package main

// Senha de acesso no lado de quem conecta: pergunta (janela ou terminal) e
// "lembrar senha" protegido pelo Windows (DPAPI, só o usuário atual decifra).

import (
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/term"

	"telaremota/internal/access"
	"telaremota/internal/config"
	"telaremota/internal/winui"
)

func dpapi(in []byte, protect bool) ([]byte, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("vazio")
	}
	blob := windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}
	var out windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&blob, nil, nil, 0, nil, 1, &out)
	} else {
		err = windows.CryptUnprotectData(&blob, nil, nil, 0, nil, 1, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func savedPasswords() map[string]string {
	m := map[string]string{}
	config.Load(userPath("senhas.json"), &m)
	return m
}

func rememberedPassword(target string) string {
	enc, ok := savedPasswords()[target]
	if !ok {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return ""
	}
	pw, err := dpapi(raw, false)
	if err != nil {
		return ""
	}
	return string(pw)
}

func rememberPassword(target, pw string) {
	m := savedPasswords()
	if pw == "" {
		delete(m, target)
	} else if enc, err := dpapi([]byte(pw), true); err == nil {
		m[target] = base64.StdEncoding.EncodeToString(enc)
	}
	config.Save(userPath("senhas.json"), m)
}

// askPassword monta a função que obtém a senha de target. remember recebe
// se o usuário pediu para lembrar.
func askPassword(target, flagPw string, gui bool, remember *bool) access.Ask {
	saved := rememberedPassword(target)
	first := true
	return func(retry bool) (string, bool) {
		if first {
			first = false
			if flagPw != "" {
				return flagPw, true
			}
			if saved != "" {
				*remember = true
				return saved, true
			}
		}
		if retry && flagPw != "" {
			return "", false // senha passada em -senha estava errada: não pergunta
		}
		if retry && saved != "" {
			rememberPassword(target, "") // a senha lembrada não vale mais
			saved = ""
		}
		if gui || !term.IsTerminal(int(os.Stdin.Fd())) {
			pw, rem, ok := passwordDialog(target, retry)
			*remember = rem
			return pw, ok
		}
		if retry {
			fmt.Println("Senha incorreta.")
		}
		fmt.Printf("Senha de acesso a %q: ", target)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil || len(b) == 0 {
			return "", false
		}
		return string(b), true
	}
}

// passwordDialog mostra uma janela pedindo a senha.
func passwordDialog(target string, retry bool) (pw string, remember, ok bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a, err := winui.New("TelaRemota — Senha de acesso", 400, 168)
	if err != nil {
		return "", false, false
	}
	title := fmt.Sprintf("Senha de acesso a %q:", target)
	if retry {
		title = "Senha incorreta. Tente novamente:"
	}
	lbl := a.Label(title, 20, 18, 360, 20)
	if retry {
		lbl.SetFont(a.Font("Segoe UI", 9, true))
	}
	edit := a.Password(20, 44, 360, 26)
	chk := a.Check("Lembrar a senha neste computador", 20, 80, 360, 22, nil)
	submit := func() {
		pw, remember, ok = strings.TrimSpace(edit.Text()), chk.Checked(), true
		if pw == "" {
			return
		}
		a.Close()
	}
	cancel := func() { ok = false; a.Close() }
	a.Button("Conectar", 196, 120, 90, 32, submit)
	a.Button("Cancelar", 292, 120, 88, 32, cancel)
	a.OnCommand(1, submit) // Enter
	a.OnCommand(2, cancel) // Esc
	edit.Focus()
	a.Run()
	if pw == "" {
		ok = false
	}
	return pw, remember, ok
}

// cmdPassword define ou remove a senha de acesso deste computador.
func cmdPassword(args []string) error {
	if err := requireAdmin(); err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "-remover" {
		if err := access.Set(config.MachineDir(), ""); err != nil {
			return err
		}
		fmt.Println("Senha de acesso removida: dispositivos pareados conectam sem senha.")
		return nil
	}
	read := func(prompt string) string {
		fmt.Print(prompt)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			b, _ := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			return string(b)
		}
		var s string
		fmt.Fscanln(os.Stdin, &s)
		fmt.Println()
		return s
	}
	p1 := read(fmt.Sprintf("Nova senha de acesso (mín. %d caracteres): ", access.MinLen))
	p2 := read("Confirme: ")
	if p1 != p2 {
		return fmt.Errorf("as senhas não conferem")
	}
	if err := access.Set(config.MachineDir(), p1); err != nil {
		return err
	}
	fmt.Println("Senha de acesso definida. Ela será pedida a quem conectar a este computador.")
	return nil
}
