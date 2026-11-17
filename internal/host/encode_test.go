package host

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

func testFrame(w, h int) []byte {
	pix := make([]byte, w*h*4)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range pix {
		pix[i] = byte(i/4%w) ^ byte(r.IntN(16))
	}
	return pix
}

func TestParallelMatchesSequential(t *testing.T) {
	w, h := 640, 360
	pix := testFrame(w, h)
	rects := DirtyRects(pix, make([]byte, len(pix)), w, h, true)
	var p ParallelEncoder
	got := p.EncodeAll(pix, w, rects, 60)
	if len(got) != len(rects) {
		t.Fatalf("%d pacotes para %d regiões", len(got), len(rects))
	}
	var e Encoder
	for i, r := range rects {
		want, err := e.Encode(pix, w, r, 60)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got[i], want) {
			t.Fatalf("região %d difere", i)
		}
	}
}

func benchFull(b *testing.B, parallel bool) {
	w, h := 1920, 1080
	pix := testFrame(w, h)
	rects := DirtyRects(pix, make([]byte, len(pix)), w, h, true)
	var p ParallelEncoder
	var e Encoder
	b.ResetTimer()
	for range b.N {
		if parallel {
			p.EncodeAll(pix, w, rects, 60)
		} else {
			for _, r := range rects {
				e.Encode(pix, w, r, 60)
			}
		}
	}
}

func BenchmarkFullFrameSequential(b *testing.B) { benchFull(b, false) }
func BenchmarkFullFrameParallel(b *testing.B)   { benchFull(b, true) }
