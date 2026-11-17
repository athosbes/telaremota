package host

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"runtime"
	"sync"
)

// TileSize é o lado do bloco usado para detectar mudanças.
const TileSize = 64

type Rect struct{ X, Y, W, H int }

// DirtyRects compara o quadro atual com o anterior em blocos 64×64 e junta
// blocos alterados vizinhos (na mesma linha) em retângulos.
func DirtyRects(cur, prev []byte, w, h int, full bool) []Rect {
	var out []Rect
	stride := w * 4
	cols := (w + TileSize - 1) / TileSize
	for ty := 0; ty < h; ty += TileSize {
		th := min(TileSize, h-ty)
		run := -1
		for c := 0; c <= cols; c++ {
			dirty := false
			if c < cols {
				tx := c * TileSize
				dirty = full || tileChanged(cur, prev, stride, tx, ty, min(TileSize, w-tx), th)
			}
			if dirty && run < 0 {
				run = c
			} else if !dirty && run >= 0 {
				x := run * TileSize
				out = append(out, Rect{x, ty, min(c*TileSize, w) - x, th})
				run = -1
			}
		}
	}
	return out
}

func tileChanged(cur, prev []byte, stride, x, y, w, h int) bool {
	for row := y; row < y+h; row++ {
		o := row*stride + x*4
		if !bytes.Equal(cur[o:o+w*4], prev[o:o+w*4]) {
			return true
		}
	}
	return false
}

// CopyRect copia a região r de src para dst (mesmo tamanho de tela).
func CopyRect(dst, src []byte, w int, r Rect) {
	stride := w * 4
	for row := r.Y; row < r.Y+r.H; row++ {
		o := row*stride + r.X*4
		copy(dst[o:o+r.W*4], src[o:o+r.W*4])
	}
}

// Encoder converte regiões BGRA em JPEG, reaproveitando buffers.
type Encoder struct {
	pix []byte
	buf bytes.Buffer
}

// Encode devolve prefix + [x,y,w,h u16] + JPEG. O slice é reutilizado na próxima chamada.
func (e *Encoder) Encode(pix []byte, w int, r Rect, quality int, prefix ...byte) ([]byte, error) {
	n := r.W * r.H * 4
	if cap(e.pix) < n {
		e.pix = make([]byte, n)
	}
	img := &image.RGBA{Pix: e.pix[:n], Stride: r.W * 4, Rect: image.Rect(0, 0, r.W, r.H)}
	stride := w * 4
	for y := 0; y < r.H; y++ {
		src := pix[(r.Y+y)*stride+r.X*4:][:r.W*4]
		dst := img.Pix[y*r.W*4:][:r.W*4]
		for i := 0; i < len(src); i += 4 {
			dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 255
		}
	}
	e.buf.Reset()
	e.buf.Write(prefix)
	var hdr [8]byte
	binary.BigEndian.PutUint16(hdr[0:], uint16(r.X))
	binary.BigEndian.PutUint16(hdr[2:], uint16(r.Y))
	binary.BigEndian.PutUint16(hdr[4:], uint16(r.W))
	binary.BigEndian.PutUint16(hdr[6:], uint16(r.H))
	e.buf.Write(hdr[:])
	if err := jpeg.Encode(&e.buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return e.buf.Bytes(), nil
}

// ParallelEncoder codifica as regiões de um quadro em paralelo (um Encoder
// por região, reaproveitado entre quadros).
type ParallelEncoder struct {
	encs []Encoder
	out  [][]byte
	sem  chan struct{}
}

// EncodeAll devolve um pacote por região, na mesma ordem (nil se falhou).
// Os slices são reutilizados na próxima chamada.
func (p *ParallelEncoder) EncodeAll(pix []byte, w int, rects []Rect, quality int) [][]byte {
	if p.sem == nil {
		p.sem = make(chan struct{}, max(1, runtime.NumCPU()-1))
	}
	if len(p.encs) < len(rects) {
		p.encs = append(p.encs, make([]Encoder, len(rects)-len(p.encs))...)
	}
	p.out = append(p.out[:0], make([][]byte, len(rects))...)
	if len(rects) == 1 {
		p.out[0], _ = p.encs[0].Encode(pix, w, rects[0], quality)
		return p.out
	}
	var wg sync.WaitGroup
	for i, r := range rects {
		p.sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-p.sem; wg.Done() }()
			p.out[i], _ = p.encs[i].Encode(pix, w, r, quality)
		}()
	}
	wg.Wait()
	return p.out
}
