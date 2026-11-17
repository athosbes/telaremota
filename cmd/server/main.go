// Servidor de sinalização do TelaRemota.
//
// Responsabilidades: registrar agentes (ID de 9 dígitos), intermediar o
// handshake WebRTC (SDP/ICE) entre visualizador e agente, entregar a página
// do visualizador e, opcionalmente, credenciais TURN temporárias.
// O conteúdo da sessão (tela, teclado, mouse) NÃO passa por aqui: vai P2P,
// criptografado com DTLS.
package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"telaremota/internal/proto"
	"telaremota/web"
)

func main() {
	var (
		addr       = flag.String("addr", ":8080", "endereço HTTP (ignorado com -domain)")
		domain     = flag.String("domain", "", "domínio para HTTPS automático via Let's Encrypt (usa portas 80 e 443)")
		certFile   = flag.String("cert", "", "certificado TLS (PEM), alternativa ao -domain")
		keyFile    = flag.String("key", "", "chave TLS (PEM)")
		dataDir    = flag.String("data", "data", "diretório de dados (IDs registrados, certificados)")
		stunURLs   = flag.String("stun", "stun:stun.l.google.com:19302,stun:stun.cloudflare.com:3478", "servidores STUN separados por vírgula")
		turnURLs   = flag.String("turn", "", "servidores TURN separados por vírgula (ex: turn:turn.exemplo.com:3478)")
		turnSecret = flag.String("turn-secret", "", "segredo compartilhado do TURN (coturn: use-auth-secret / static-auth-secret)")
		downloads  = flag.String("downloads", "", "diretório servido em /download/ (ex: para distribuir o agente)")
		behindProx = flag.Bool("proxy", false, "confiar no cabeçalho X-Forwarded-For (use atrás de proxy reverso)")
	)
	flag.Parse()
	log.SetFlags(log.LstdFlags)

	st, err := openStore(filepath.Join(*dataDir, "agents.json"))
	if err != nil {
		log.Fatalf("erro ao abrir dados: %v", err)
	}

	h := newHub(st, makeICE(split(*stunURLs), split(*turnURLs), *turnSecret))
	h.trustProxy = *behindProx

	static, _ := fs.Sub(web.Static, "static")
	files := http.FileServer(http.FS(static))

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.serveWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	if *downloads != "" {
		mux.Handle("/download/", http.StripPrefix("/download/", http.FileServer(noListing{http.Dir(*downloads)})))
	}
	mux.Handle("/", securityHeaders(files))

	switch {
	case *domain != "":
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*domain),
			Cache:      autocert.DirCache(filepath.Join(*dataDir, "certs")),
		}
		go func() {
			log.Printf("HTTP :80 (desafio ACME + redirecionamento)")
			log.Fatal(http.ListenAndServe(":80", m.HTTPHandler(nil)))
		}()
		srv := &http.Server{Addr: ":443", Handler: mux, TLSConfig: m.TLSConfig(), ReadHeaderTimeout: 10 * time.Second}
		log.Printf("TelaRemota servidor em https://%s", *domain)
		log.Fatal(srv.ListenAndServeTLS("", ""))
	case *certFile != "":
		srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		log.Printf("TelaRemota servidor (TLS) em %s", *addr)
		log.Fatal(srv.ListenAndServeTLS(*certFile, *keyFile))
	default:
		srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		log.Printf("TelaRemota servidor em http://localhost%s  (sem TLS: use apenas para testes locais)", portOf(*addr))
		log.Fatal(srv.ListenAndServe())
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' blob: data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.TLS != nil {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// noListing impede listagem de diretório em /download/.
type noListing struct{ fs http.FileSystem }

func (n noListing) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if s, _ := f.Stat(); s != nil && s.IsDir() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// makeICE monta a lista de servidores ICE por sessão. Com -turn-secret gera
// credenciais temporárias no padrão "TURN REST API" (compatível com coturn).
func makeICE(stun, turn []string, secret string) func(sid string) []proto.ICEServer {
	return func(sid string) []proto.ICEServer {
		var out []proto.ICEServer
		if len(stun) > 0 {
			out = append(out, proto.ICEServer{URLs: stun})
		}
		if len(turn) > 0 {
			s := proto.ICEServer{URLs: turn}
			if secret != "" {
				user := fmt.Sprintf("%d:%s", time.Now().Add(12*time.Hour).Unix(), sid)
				m := hmac.New(sha1.New, []byte(secret))
				m.Write([]byte(user))
				s.Username = user
				s.Credential = base64.StdEncoding.EncodeToString(m.Sum(nil))
			}
			out = append(out, s)
		}
		return out
	}
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return ":" + p
	}
	return addr
}
