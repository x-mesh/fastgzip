// Package fastgzip decodes gzip (RFC 1952) and DEFLATE (RFC 1951) data in Go,
// without cgo.
//
// Reader replaces compress/gzip.Reader for reading: NewReader, Read, Reset and
// Close behave the same, and concatenated members are read as one stream.
// Reader does not expose the gzip header fields (Name, Comment, ModTime).
// Gunzip and Inflate decode data that is already in memory.
//
// The decoder uses a 64-bit bit buffer refilled with one 8-byte load, table
// entries that count their extra bits so one shift consumes a code and its
// extra bits, table loads overlapped with refills, decoding the next symbol
// before a match copy, and wide match copies. On arm64 the CRC-32 runs three CRC32X chains in parallel
// in assembly; elsewhere it uses hash/crc32.
package fastgzip
