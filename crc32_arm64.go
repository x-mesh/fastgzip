package fastgzip

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"runtime"
)

//go:noescape
func crc32pmull(crc uint32, p *byte, n int) uint32

var hasPMULL = detectPMULL()

// detectPMULL reports PMULL and the CRC32 instructions, which crc32pmull
// both needs.
func detectPMULL() bool {
	switch runtime.GOOS {
	case "darwin", "ios":
		// every Apple arm64 core has both
		return true
	case "linux", "android":
		// AT_HWCAP (16): HWCAP_PMULL (1 << 4), HWCAP_CRC32 (1 << 7)
		const want = 1<<4 | 1<<7
		auxv, err := os.ReadFile("/proc/self/auxv")
		if err != nil {
			return false
		}
		for i := 0; i+16 <= len(auxv); i += 16 {
			if binary.LittleEndian.Uint64(auxv[i:]) == 16 {
				return binary.LittleEndian.Uint64(auxv[i+8:])&want == want
			}
		}
	}
	return false
}

func crcUpdate(crc uint32, p []byte) uint32 {
	if n := len(p) &^ 127; hasPMULL && n > 0 {
		crc = ^crc32pmull(^crc, &p[0], n)
		p = p[n:]
	}
	return crc32.Update(crc, crc32.IEEETable, p)
}
