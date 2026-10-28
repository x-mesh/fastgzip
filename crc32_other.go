//go:build !arm64

package fastgzip

import "hash/crc32"

// On amd64 hash/crc32 already folds with PCLMULQDQ.
func crcUpdate(crc uint32, p []byte) uint32 {
	return crc32.Update(crc, crc32.IEEETable, p)
}
