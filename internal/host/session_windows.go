//go:build windows

// Package host atende uma sessão de controle remoto: captura a tela, envia
// as regiões alteradas e injeta mouse/teclado. Funciona sobre qualquer par
// leitor/escritor (stream QUIC ou pipes do processo auxiliar do serviço).
package host

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"telaremota/internal/clipboard"
	"telaremota/internal/input"
	"telaremota/internal/screen"
	"telaremota/internal/wire"
)

const (
	fps         = 60
	maxInflight = 3 // quadros enviados sem confirmação (limita o atraso em conexões lentas)
)

type event struct {
	t    byte
	a, b int
}

type session struct {
	out       *wire.Writer
	done      chan struct{}
	stopOnce  sync.Once
	inputs    chan event
	ackCh     chan struct{}
	inflight  atomic.Int32
	quality   atomic.Int32
	forceFull atomic.Bool
	sw, sh    atomic.Int32 // tamanho da região capturada
	ox, oy    atomic.Int32 // canto da região na área de trabalho virtual
	monitor   atomic.Int32 // tela pedida pelo visualizador (-1 = principal)
	clipSkip  atomic.Uint32
}

// Serve atende a sessão até r terminar (visualizador desconectou).
func Serve(r io.Reader, w io.Writer, from string) error {
	s := &session{
		out:    wire.NewWriter(w),
		done:   make(chan struct{}),
		inputs: make(chan event, 1024),
		ackCh:  make(chan struct{}, 1),
	}
	s.quality.Store(60)
	s.monitor.Store(-1)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.inputLoop() }()
	go func() { defer wg.Done(); s.captureLoop() }()
	go func() { defer wg.Done(); s.clipLoop() }()
	log.Printf("sessão iniciada por %q", from)
	err := s.readLoop(bufio.NewReaderSize(r, 64<<10))
	s.stop()
	wg.Wait()
	log.Printf("sessão de %q encerrada", from)
	if err == io.EOF {
		err = nil
	}
	return err
}

func (s *session) stop() { s.stopOnce.Do(func() { close(s.done) }) }

func (s *session) isDone() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *session) push(ev event) {
	select {
	case s.inputs <- ev:
	case <-s.done:
	}
}

func (s *session) readLoop(br *bufio.Reader) error {
	for {
		t, p, err := wire.Read(br)
		if err != nil {
			return err
		}
		u16 := func(i int) int { return int(binary.BigEndian.Uint16(p[i:])) }
		i16 := func(i int) int { return int(int16(binary.BigEndian.Uint16(p[i:]))) }
		switch t {
		case wire.Move:
			if len(p) >= 4 {
				s.push(event{t, u16(0), u16(2)})
			}
		case wire.Button:
			if len(p) >= 2 {
				s.push(event{t, int(p[0]), int(p[1])})
			}
		case wire.Wheel:
			if len(p) >= 4 {
				s.push(event{t, i16(0), i16(2)})
			}
		case wire.Key:
			if len(p) >= 3 {
				s.push(event{t, u16(0), int(p[2])})
			}
		case wire.CAD:
			s.push(event{t: t})
		case wire.Ack:
			if s.inflight.Add(-1) < 0 {
				s.inflight.Store(0)
			}
			select {
			case s.ackCh <- struct{}{}:
			default:
			}
		case wire.Quality:
			if len(p) >= 1 {
				s.quality.Store(int32(max(20, min(95, int(p[0])))))
				s.forceFull.Store(true)
			}
		case wire.Refresh:
			s.forceFull.Store(true)
		case wire.Monitor:
			if len(p) >= 1 {
				s.monitor.Store(int32(p[0]))
			}
		case wire.ClipV:
			text := string(p)
			go func() {
				if clipboard.Set(text) == nil {
					s.clipSkip.Store(clipboard.Seq())
				}
			}()
		}
	}
}

// inputLoop injeta eventos numa thread presa à área de trabalho de entrada.
func (s *session) inputLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var d desktop
	d.sync()
	defer d.close()
	inj := input.New()
	defer inj.ReleaseAll()
	lastSync := time.Now()
	for {
		select {
		case <-s.done:
			return
		case ev := <-s.inputs:
			if time.Since(lastSync) > 100*time.Millisecond {
				if d.sync() {
					inj.ReleaseAll() // teclas presas não atravessam a troca de área de trabalho
				}
				lastSync = time.Now()
			}
			switch ev.t {
			case wire.Move:
				w, h := int(s.sw.Load()), int(s.sh.Load())
				if w == 0 {
					w, h = screen.Size()
				}
				x := min(max(ev.a, 0), w-1) + int(s.ox.Load())
				y := min(max(ev.b, 0), h-1) + int(s.oy.Load())
				vx, vy, vw, vh := screen.VirtualScreen()
				inj.MouseMoveVirtual(x-vx, y-vy, vw, vh)
			case wire.Button:
				inj.Button(ev.a, ev.b != 0)
			case wire.Wheel:
				inj.Wheel(ev.a, ev.b)
			case wire.Key:
				inj.Scan(uint16(ev.a), ev.b != 0)
			case wire.CAD:
				inj.ReleaseAll()
				if err := sendCAD(); err != nil {
					s.out.Send(wire.Info, []byte("Ctrl+Alt+Del indisponível: "+err.Error()))
				}
			}
		}
	}
}

func (s *session) captureLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var d desktop
	d.sync()
	defer d.close()

	var (
		cp       *screen.Capturer
		prev     []byte
		pw, ph   int
		frameID  uint32
		enc      ParallelEncoder
		lastSync time.Time
		failing  bool
		mons     []screen.Monitor
		lastMons time.Time
		sentMons []byte
		applied  int32
	)
	defer func() {
		if cp != nil {
			cp.Close()
		}
	}()
	interval := time.Second / fps
	for !s.isDone() {
		if s.inflight.Load() >= maxInflight {
			select {
			case <-s.ackCh:
			case <-time.After(3 * time.Second):
				s.inflight.Store(0)
			case <-s.done:
				return
			}
			continue
		}
		start := time.Now()
		if time.Since(lastSync) > 250*time.Millisecond {
			if d.sync() && cp != nil {
				cp.Close() // DC pertence à área de trabalho anterior
				cp = nil
				s.forceFull.Store(true)
			}
			lastSync = time.Now()
		}
		if cp == nil {
			var err error
			if cp, err = screen.NewCapturer(); err != nil {
				cp = nil
				s.sleep(500 * time.Millisecond)
				continue
			}
			lastMons = time.Time{}
		}
		if want := s.monitor.Load(); time.Since(lastMons) > time.Second || want != applied {
			if time.Since(lastMons) > time.Second {
				mons = screen.Monitors()
				lastMons = time.Now()
			}
			applied = want
			sel := s.pickMonitor(cp, mons, int(want))
			if msg := monitorsMsg(mons, sel); !bytes.Equal(msg, sentMons) {
				sentMons = msg
				s.forceFull.Store(true)
				if s.out.Send(wire.Monitors, msg) != nil {
					s.stop()
					return
				}
			}
		}
		pix, w, h, err := cp.Capture()
		if err != nil {
			if !failing {
				failing = true
				s.out.Send(wire.Info, []byte("Tela indisponível (tela de bloqueio ou UAC sem o serviço instalado)"))
			}
			s.sleep(300 * time.Millisecond)
			continue
		}
		if failing {
			failing = false
			s.out.Send(wire.Info, nil)
		}
		full := s.forceFull.Swap(false)
		if w != pw || h != ph || prev == nil {
			pw, ph = w, h
			prev = make([]byte, len(pix))
			s.sw.Store(int32(w))
			s.sh.Store(int32(h))
			if s.out.Send(wire.Size, wire.U16(w), wire.U16(h)) != nil {
				s.stop()
				return
			}
			full = true
		}
		rects := DirtyRects(pix, prev, w, h, full)
		if len(rects) > 0 {
			packets := enc.EncodeAll(pix, w, rects, int(s.quality.Load()))
			for i, r := range rects {
				if packets[i] == nil {
					continue
				}
				if s.out.Send(wire.Rect, packets[i]) != nil {
					s.stop()
					return
				}
				CopyRect(prev, pix, w, r)
			}
			frameID++
			s.inflight.Add(1)
			if s.out.Send(wire.FrameEnd, wire.U32(frameID)) != nil {
				s.stop()
				return
			}
		}
		s.sleep(interval - time.Since(start))
	}
}

// pickMonitor aponta a captura para a tela pedida (ou a principal) e devolve o
// índice efetivamente mostrado (wire.MonitorAll = todas).
func (s *session) pickMonitor(cp *screen.Capturer, mons []screen.Monitor, want int) int {
	var x, y, w, h, sel int
	switch {
	case want == wire.MonitorAll && len(mons) > 1:
		x, y, w, h = screen.VirtualScreen()
		sel = wire.MonitorAll
	case want >= 0 && want < len(mons):
		m := mons[want]
		x, y, w, h, sel = m.X, m.Y, m.W, m.H, want
	default:
		sel = -1
		for i, m := range mons {
			if m.Primary {
				x, y, w, h, sel = m.X, m.Y, m.W, m.H, i
			}
		}
		if sel < 0 { // sem lista (falha na enumeração): monitor principal
			w, h = screen.Size()
			sel = 0
		}
	}
	if w > 0 && h > 0 {
		cp.SetRegion(x, y, w, h)
		s.ox.Store(int32(x))
		s.oy.Store(int32(y))
	}
	return sel
}

func monitorsMsg(mons []screen.Monitor, sel int) []byte {
	b := []byte{byte(len(mons)), byte(sel)}
	for _, m := range mons {
		var f byte
		if m.Primary {
			f = 1
		}
		b = append(b, wire.U16(m.X)...)
		b = append(b, wire.U16(m.Y)...)
		b = append(b, wire.U16(m.W)...)
		b = append(b, wire.U16(m.H)...)
		b = append(b, f)
	}
	return b
}

func (s *session) sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-time.After(d):
	case <-s.done:
	}
}

// clipLoop envia ao visualizador o texto copiado neste computador.
func (s *session) clipLoop() {
	last := clipboard.Seq()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		n := clipboard.Seq()
		if n == last {
			continue
		}
		last = n
		if n == s.clipSkip.Load() {
			continue
		}
		if text, err := clipboard.Get(); err == nil && text != "" {
			s.out.Send(wire.ClipH, []byte(text))
		}
	}
}
