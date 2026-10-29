package fastgzip

import (
	"hash/crc32"
	"math/rand/v2"
	"testing"
)

func TestCRCUpdate(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	buf := make([]byte, 1<<20+77)
	for i := range buf {
		buf[i] = byte(r.Uint32())
	}
	// every length across the 128-byte blocks of crc32pmull, at unaligned
	// starts
	for n := range 1100 {
		for _, off := range []int{0, 1, 7} {
			for _, init := range []uint32{0, 0xdeadbeef} {
				p := buf[off : off+n]
				want := crc32.Update(init, crc32.IEEETable, p)
				if got := crcUpdate(init, p); got != want {
					t.Fatalf("n=%d off=%d init=%#x: got %#x want %#x", n, off, init, got, want)
				}
			}
		}
	}
	if got, want := crcUpdate(0, buf), crc32.ChecksumIEEE(buf); got != want {
		t.Fatalf("n=%d: got %#x want %#x", len(buf), got, want)
	}
	// chained updates over uneven pieces
	var got, want uint32
	for p := buf; len(p) > 0; {
		k := min(len(p), r.IntN(200000))
		got = crcUpdate(got, p[:k])
		want = crc32.Update(want, crc32.IEEETable, p[:k])
		p = p[k:]
	}
	if got != want {
		t.Fatalf("chained: got %#x want %#x", got, want)
	}
}

func BenchmarkCRC(b *testing.B) {
	buf := make([]byte, 4<<20)
	for i := range buf {
		buf[i] = byte(i * 7)
	}
	b.Run("std", func(b *testing.B) {
		b.SetBytes(int64(len(buf)))
		for b.Loop() {
			crc32.Update(0, crc32.IEEETable, buf)
		}
	})
	b.Run("fastgzip", func(b *testing.B) {
		b.SetBytes(int64(len(buf)))
		for b.Loop() {
			crcUpdate(0, buf)
		}
	})
}
