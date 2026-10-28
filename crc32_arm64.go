package fastgzip

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"runtime"
	"sync"
)

//go:noescape
func crc32x3(c *[3]uint32, p *byte, n int)

// crcLane is the bytes per chain in one crc32x3 call. Combining the three
// chains costs 8 table lookups, small next to 48 KB of checksum work.
const crcLane = 16 << 10

var (
	hasCRC32 = detectCRC32()
	// shift1 and shift2 multiply a CRC state by x^(8*crcLane) and
	// x^(16*crcLane): the effect of crcLane or 2*crcLane zero bytes
	shift1, shift2 *[4][256]uint32
	shiftOnce      sync.Once
)

func detectCRC32() bool {
	switch runtime.GOOS {
	case "darwin", "ios":
		// every Apple arm64 core has the CRC32 instructions
		return true
	case "linux", "android":
		// AT_HWCAP (16), HWCAP_CRC32 (1 << 7)
		auxv, err := os.ReadFile("/proc/self/auxv")
		if err != nil {
			return false
		}
		for i := 0; i+16 <= len(auxv); i += 16 {
			if binary.LittleEndian.Uint64(auxv[i:]) == 16 {
				return binary.LittleEndian.Uint64(auxv[i+8:])&(1<<7) != 0
			}
		}
	}
	return false
}

func crcUpdate(crc uint32, p []byte) uint32 {
	if !hasCRC32 || len(p) < 3*crcLane {
		return crc32.Update(crc, crc32.IEEETable, p)
	}
	shiftOnce.Do(func() {
		shift1 = shiftTable(crcLane)
		shift2 = shiftTable(2 * crcLane)
	})
	s := ^crc
	for len(p) >= 3*crcLane {
		c := [3]uint32{s, 0, 0}
		crc32x3(&c, &p[0], crcLane)
		s = shiftBy(shift2, c[0]) ^ shiftBy(shift1, c[1]) ^ c[2]
		p = p[3*crcLane:]
	}
	return crc32.Update(^s, crc32.IEEETable, p)
}

func shiftBy(t *[4][256]uint32, s uint32) uint32 {
	return t[0][byte(s)] ^ t[1][byte(s>>8)] ^ t[2][byte(s>>16)] ^ t[3][s>>24]
}

// shiftTable tabulates s -> s * x^(8n) mod P one byte of s at a time; the
// map is linear, so the four lookups XOR together.
func shiftTable(n int) *[4][256]uint32 {
	k := xpow8n(n)
	t := new([4][256]uint32)
	for i := range t {
		for b := range 256 {
			t[i][b] = multModP(k, uint32(b)<<(8*i))
		}
	}
	return t
}

// multModP and xpow8n are ports of multmodp and x2nmodp from crc32.c in zlib
// 1.2.12 (Copyright (C) 1995-2022 Mark Adler; see NOTICE), for the reflected
// IEEE polynomial.
const ieeePoly = 0xedb88320

func multModP(a, b uint32) uint32 {
	m := uint32(1) << 31
	var p uint32
	for {
		if a&m != 0 {
			p ^= b
			if a&(m-1) == 0 {
				break
			}
		}
		m >>= 1
		if b&1 != 0 {
			b = b>>1 ^ ieeePoly
		} else {
			b >>= 1
		}
	}
	return p
}

// xpow8n returns x^(8n) mod P.
func xpow8n(n int) uint32 {
	p := uint32(1) << 31  // x^0
	sq := uint32(1) << 23 // x^8
	for ; n > 0; n >>= 1 {
		if n&1 != 0 {
			p = multModP(sq, p)
		}
		sq = multModP(sq, sq)
	}
	return p
}
