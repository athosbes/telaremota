// Package wire define o protocolo binário da sessão (sobre um stream QUIC
// ou pipe) e utilitários de mensagens JSON delimitadas por linha.
//
// Quadro: [tipo u8][tamanho u32 BE][dados]
package wire

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// Agente -> visualizador
const (
	Rect     byte = 1 // x,y,w,h u16 + JPEG
	FrameEnd byte = 2 // id u32
	Size     byte = 3 // w,h u16
	ClipH    byte = 4 // texto UTF-8 copiado no computador remoto
	Info     byte = 5 // texto informativo
	// Monitors lista as telas: n u8, atual u8 (MonitorAll = todas) e, para
	// cada tela, x,y i16 + w,h u16 + flags u8 (1 = principal).
	Monitors byte = 6
)

// MonitorAll seleciona a área de trabalho inteira (todas as telas).
const MonitorAll = 255

// Visualizador -> agente
const (
	Move    byte = 10 // x,y u16
	Button  byte = 11 // botão u8, pressionado u8
	Wheel   byte = 12 // dy,dx i16 (120 = 1 dente; dy>0 = para cima)
	Key     byte = 13 // scancode u16 (0xE0xx = estendida, 0xE11D = Pause), pressionado u8
	Ack     byte = 14 // id u32
	Quality byte = 15 // 20..95
	Refresh byte = 16
	CAD     byte = 17 // Ctrl+Alt+Del
	ClipV   byte = 18 // texto UTF-8 copiado no visualizador
	Monitor byte = 19 // índice u8 da tela a mostrar (MonitorAll = todas)
)

const MaxFrame = 32 << 20

// Writer serializa quadros de várias goroutines.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

func (w *Writer) Send(t byte, parts ...[]byte) error {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	buf := make([]byte, 5, 5+n)
	buf[0] = t
	binary.BigEndian.PutUint32(buf[1:], uint32(n))
	for _, p := range parts {
		buf = append(buf, p...)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.w.Write(buf)
	return err
}

// Read lê um quadro.
func Read(r *bufio.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrame {
		return 0, nil, errors.New("quadro grande demais")
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return hdr[0], p, nil
}

func U16(v int) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(v))
	return b[:]
}

func U32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

// WriteJSON envia um objeto JSON terminado em \n.
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadJSON lê uma linha JSON (máx. 64 KB).
func ReadJSON(r *bufio.Reader, v any) error {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return err
		}
		line = append(line, chunk...)
		if len(line) > 64<<10 {
			return errors.New("linha grande demais")
		}
		if !isPrefix {
			break
		}
	}
	return json.Unmarshal(line, v)
}

// Reply é a resposta padrão de controle.
type Reply struct {
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}
