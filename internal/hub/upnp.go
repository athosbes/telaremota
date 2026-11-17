package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
)

type igd interface {
	AddPortMappingCtx(ctx context.Context, remoteHost string, extPort uint16, proto string, intPort uint16, intClient string, enabled bool, desc string, lease uint32) error
	DeletePortMappingCtx(ctx context.Context, remoteHost string, extPort uint16, proto string) error
	GetExternalIPAddressCtx(ctx context.Context) (string, error)
	LocalAddr() net.IP
}

type candidate struct {
	gw   igd
	host string // endereço do roteador na rede local
}

// findIGDs lista todos os roteadores UPnP da rede (pode haver mais de um:
// roteador principal + roteador/ponto de acesso secundário).
func findIGDs(ctx context.Context) []candidate {
	var out []candidate
	add := func(gw igd, loc *url.URL) {
		h := ""
		if loc != nil {
			h = loc.Hostname()
		}
		out = append(out, candidate{gw, h})
	}
	if cs, _, err := internetgateway2.NewWANIPConnection2ClientsCtx(ctx); err == nil {
		for _, c := range cs {
			add(c, c.Location)
		}
	}
	if cs, _, err := internetgateway2.NewWANIPConnection1ClientsCtx(ctx); err == nil {
		for _, c := range cs {
			add(c, c.Location)
		}
	}
	if cs, _, err := internetgateway2.NewWANPPPConnection1ClientsCtx(ctx); err == nil {
		for _, c := range cs {
			add(c, c.Location)
		}
	}
	return out
}

// mapPort abre a porta UDP no roteador que dá acesso à internet e a renova
// periodicamente até ctx terminar.
func mapPort(ctx context.Context, port int, logf func(string, ...any)) (publicIP string, err error) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	cands := findIGDs(dctx)
	cancel()
	if len(cands) == 0 {
		return "", errors.New("nenhum roteador com UPnP encontrado")
	}
	var problems []string
	for _, c := range cands {
		ip, err := tryMap(c.gw, port)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", c.host, err))
			continue
		}
		logf("UPnP: porta UDP %d aberta no roteador %s", port, c.host)
		go renew(ctx, c.gw, port, logf)
		return ip, nil
	}
	return "", errors.New(strings.Join(problems, "; "))
}

func tryMap(gw igd, port int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ext, err := gw.GetExternalIPAddressCtx(ctx)
	if err != nil {
		return "", fmt.Errorf("não respondeu (%v)", shortErr(err))
	}
	if ip := net.ParseIP(ext); ip == nil || ip.IsPrivate() || isCGNAT(ip) || ip.IsUnspecified() {
		return "", fmt.Errorf("não é a saída para a internet (IP externo %q)", ext)
	}
	if err := addMapping(gw, port); err != nil {
		return "", fmt.Errorf("recusou o mapeamento (%v)", shortErr(err))
	}
	return ext, nil
}

func addMapping(gw igd, port int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	local := gw.LocalAddr().String()
	err := gw.AddPortMappingCtx(ctx, "", uint16(port), "UDP", uint16(port), local, true, "TelaRemota", 3600)
	if err != nil { // alguns roteadores só aceitam concessão permanente
		err = gw.AddPortMappingCtx(ctx, "", uint16(port), "UDP", uint16(port), local, true, "TelaRemota", 0)
	}
	return err
}

func renew(ctx context.Context, gw igd, port int, logf func(string, ...any)) {
	t := time.NewTicker(20 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			gw.DeletePortMappingCtx(c, "", uint16(port), "UDP")
			cancel()
			return
		case <-t.C:
			if err := addMapping(gw, port); err != nil {
				logf("UPnP: falha ao renovar a porta: %v", err)
			}
		}
	}
}

func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

func shortErr(err error) string {
	s := err.Error()
	if strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timeout") {
		return "tempo esgotado"
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// localAddrs lista endereços IPv6 globais e IPv4 de rede local.
func localAddrs() (v6, lan []string) {
	v6, lan, _ = LocalAddrs()
	return
}

// LocalAddrs lista IPv6 globais, IPv4 de rede local e endereços de redes
// privadas virtuais como Tailscale/ZeroTier (faixa 100.64.0.0/10).
func LocalAddrs() (v6, lan, vpn []string) {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		isVPN := strings.Contains(strings.ToLower(i.Name), "tailscale") || strings.Contains(strings.ToLower(i.Name), "zerotier")
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP
			switch {
			case ip.To4() != nil && (isCGNAT(ip) || isVPN):
				vpn = append(vpn, ip.String())
			case ip.To4() != nil && ip.IsPrivate():
				lan = append(lan, ip.String())
			case ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !isVPN:
				v6 = append(v6, ip.String())
			}
		}
	}
	return
}
