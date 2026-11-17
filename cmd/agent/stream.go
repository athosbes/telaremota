package main

import (
	"encoding/binary"
	"log"
	"time"

	"telaremota/internal/host"
	"telaremota/internal/screen"
)

// Protocolo do canal "video" (binário, ordenado, confiável).
// Cada mensagem do DataChannel = [último? 1 byte][fatia]. As fatias são
// concatenadas até "último=1", formando uma mensagem de aplicação:
//
//	1 retângulo : [1][x u16][y u16][w u16][h u16][JPEG]
//	2 fim quadro: [2][id u32]   -> visualizador responde {"t":"ack","f":id}
//	3 resolução : [3][w u16][h u16]
const (
	chunkSize   = 16 * 1024
	maxInflight = 2 // quadros enviados sem confirmação (controle de fluxo)
	fps         = 30
)

func screenSize() (int, int) { return screen.Size() }

func (s *session) stream() {
	select {
	case <-s.videoOpen:
	case <-time.After(10 * time.Second):
		s.close("canal de vídeo não abriu")
		return
	case <-s.done:
		return
	}
	cp, err := screen.NewCapturer()
	if err != nil {
		s.close("erro na captura de tela: " + err.Error())
		return
	}
	defer cp.Close()

	var (
		prev    []byte
		pw, ph  int
		frameID uint32
		enc     host.Encoder
		lastErr time.Time
	)
	interval := time.Second / fps
	for !s.isDone() {
		if s.inflight.Load() >= maxInflight {
			select {
			case <-s.ackCh:
			case <-time.After(3 * time.Second):
				s.inflight.Store(0) // visualizador travou? retoma
			case <-s.done:
				return
			}
			continue
		}
		start := time.Now()
		pix, w, h, err := cp.Capture()
		if err != nil {
			if time.Since(lastErr) > 10*time.Second {
				log.Printf("captura: %v (tela bloqueada ou UAC?)", err)
				lastErr = time.Now()
			}
			s.sleep(250 * time.Millisecond)
			continue
		}
		full := s.forceFull.Swap(false)
		if w != pw || h != ph || prev == nil {
			pw, ph = w, h
			prev = make([]byte, len(pix))
			s.sw.Store(int32(w))
			s.sh.Store(int32(h))
			msg := make([]byte, 5)
			msg[0] = 3
			binary.BigEndian.PutUint16(msg[1:], uint16(w))
			binary.BigEndian.PutUint16(msg[3:], uint16(h))
			if s.sendVideo(msg) != nil {
				return
			}
			full = true
		}
		rects := host.DirtyRects(pix, prev, w, h, full)
		if len(rects) > 0 {
			q := int(s.quality.Load())
			for _, r := range rects {
				data, err := enc.Encode(pix, w, r, q, 1)
				if err != nil {
					continue
				}
				if err := s.sendVideo(data); err != nil {
					s.close("erro ao enviar vídeo")
					return
				}
				host.CopyRect(prev, pix, w, r)
			}
			frameID++
			s.inflight.Add(1)
			end := make([]byte, 5)
			end[0] = 2
			binary.BigEndian.PutUint32(end[1:], frameID)
			if s.sendVideo(end) != nil {
				return
			}
		}
		s.sleep(interval - time.Since(start))
	}
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

func (s *session) sendVideo(msg []byte) error {
	s.mu.Lock()
	dc := s.video
	s.mu.Unlock()
	for off := 0; ; {
		n := min(chunkSize, len(msg)-off)
		buf := make([]byte, n+1)
		if off+n == len(msg) {
			buf[0] = 1
		}
		copy(buf[1:], msg[off:off+n])
		if err := dc.Send(buf); err != nil {
			return err
		}
		off += n
		if off == len(msg) {
			return nil
		}
	}
}
