package tailnet

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"telaremota/internal/identity"
)

// TestRelayConnQUIC passa uma conexão QUIC real pelo relayConn, com um relay
// falso que entrega o destino 100.64.0.1 a um servidor em 127.0.0.1.
func TestRelayConnQUIC(t *testing.T) {
	srvID, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cliID, _ := identity.LoadOrCreate(t.TempDir())
	cfg := &quic.Config{InitialPacketSize: 1200, DisablePathMTUDiscovery: true, MaxIdleTimeout: 5 * time.Second}

	ln, err := quic.ListenAddr("127.0.0.1:0", srvID.ServerTLS(func(string) error { return nil }), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srvAddr := ln.Addr().(*net.UDPAddr).AddrPort()
	go func() { // eco
		c, err := ln.Accept(context.Background())
		if err != nil {
			return
		}
		s, err := c.AcceptStream(context.Background())
		if err != nil {
			return
		}
		io.Copy(s, s)
		s.Close()
	}()

	// Relay falso: mesmo protocolo do ServeRelay, destino 100.64.0.1 -> servidor local.
	fakeTS := netip.MustParseAddrPort("100.64.0.1:47070")
	rl, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer rl.Close()
	up, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer up.Close()
	var client netip.AddrPort
	go func() {
		b := make([]byte, 65535)
		for {
			n, src, err := rl.ReadFromUDPAddrPort(b)
			if err != nil {
				return
			}
			client = src
			dst := netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[:4])), uint16(b[4])<<8|uint16(b[5]))
			if dst != fakeTS {
				t.Errorf("destino inesperado %v", dst)
				continue
			}
			up.WriteToUDPAddrPort(b[hdr:n], srvAddr)
		}
	}()
	go func() {
		b := make([]byte, 65535)
		for {
			n, _, err := up.ReadFromUDPAddrPort(b[hdr:])
			if err != nil {
				return
			}
			a := fakeTS.Addr().As4()
			copy(b, a[:])
			b[4], b[5] = byte(fakeTS.Port()>>8), byte(fakeTS.Port())
			rl.WriteToUDPAddrPort(b[:hdr+n], client)
		}
	}()

	c, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	pc := &relayConn{UDPConn: c, relay: rl.LocalAddr().(*net.UDPAddr)}
	tr := &quic.Transport{Conn: pc}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := tr.Dial(ctx, net.UDPAddrFromAddrPort(fakeTS), cliID.ClientTLS(srvID.FP), cfg)
	if err != nil {
		t.Fatalf("dial pelo relay: %v", err)
	}
	s, _ := conn.OpenStreamSync(ctx)
	msg := make([]byte, 200_000) // vários pacotes
	for i := range msg {
		msg[i] = byte(i)
	}
	go func() { s.Write(msg); s.Close() }()
	got, err := io.ReadAll(s)
	if err != nil || len(got) != len(msg) {
		t.Fatalf("eco: %d bytes, err %v", len(got), err)
	}
	conn.CloseWithError(0, "")
}

func TestIsTailnetIP(t *testing.T) {
	for s, want := range map[string]bool{"100.64.0.1": true, "100.127.255.1": true, "100.128.0.1": false, "192.168.0.1": false} {
		if IsTailnetIP(netip.MustParseAddr(s)) != want {
			t.Errorf("%s: esperado %v", s, want)
		}
	}
	if _, ok := tailnetAddr("empresa:47070"); ok {
		t.Error("nome não deveria ir pelo tailnet")
	}
}
