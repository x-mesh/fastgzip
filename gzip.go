package fastgzip

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"sync"
)

var (
	// ErrHeader reports data that does not start with a gzip header.
	ErrHeader = errors.New("fastgzip: invalid header")
	// ErrChecksum reports a gzip member whose CRC-32 or size does not match
	// its trailer.
	ErrChecksum = errors.New("fastgzip: invalid checksum")

	errClosed = errors.New("fastgzip: read after Close")
)

const (
	flagHCRC    = 1 << 1
	flagExtra   = 1 << 2
	flagName    = 1 << 3
	flagComment = 1 << 4

	// maxHeaderString bounds a name or comment with its NUL terminator, as
	// compress/gzip does: a longer one is ErrHeader.
	maxHeaderString = 512
)

// Gunzip decodes every gzip member in src and returns the concatenated data.
func Gunzip(src []byte) ([]byte, error) {
	size := 0
	if len(src) >= 4 {
		// ISIZE of the last member is only a hint: it is mod 2^32 and
		// covers one member
		size = int(binary.LittleEndian.Uint32(src[len(src)-4:]))
	}
	out := make([]byte, 0, size+fastOutMargin)
	for len(src) > 0 {
		h, err := headerLen(src)
		if err != nil {
			return nil, err
		}
		start := len(out)
		var (
			n   int
			sum uint32
		)
		if size >= concurrentCRCSize {
			out, n, sum, err = inflateConcurrentCRC(out, src[h:])
		} else {
			out, n, err = Inflate(out, src[h:])
			sum = crcUpdate(0, out[start:])
		}
		if err != nil {
			return nil, err
		}
		t := src[h+n:]
		if len(t) < 8 {
			return nil, errEOF
		}
		if sum != binary.LittleEndian.Uint32(t) ||
			uint32(len(out)-start) != binary.LittleEndian.Uint32(t[4:]) {
			return nil, ErrChecksum
		}
		src = t[8:]
	}
	return out, nil
}

// Above this size Gunzip computes the checksum on another goroutine, one
// block behind the decoder. Even at 25 GB/s the checksum is a quarter of the
// time for highly compressible data (40 MB from 600 KB: 8.1 to 6.2 ms on an
// M1 Pro); below this size the goroutine costs more than it saves.
const concurrentCRCSize = 1 << 20

func inflateConcurrentCRC(dst, src []byte) ([]byte, int, uint32, error) {
	blocks := make(chan []byte, 64)
	sum := make(chan uint32)
	go func() {
		var c uint32
		for b := range blocks {
			c = crcUpdate(c, b)
		}
		sum <- c
	}()
	out, n, err := inflate(dst, src, blocks)
	close(blocks)
	return out, n, <-sum, err
}

func headerLen(b []byte) (int, error) {
	if len(b) < 10 || b[0] != 0x1f || b[1] != 0x8b || b[2] != 8 {
		return 0, ErrHeader
	}
	flg := b[3]
	i := 10
	if flg&flagExtra != 0 {
		if len(b) < i+2 {
			return 0, errEOF
		}
		i += 2 + int(binary.LittleEndian.Uint16(b[i:]))
	}
	if i > len(b) {
		return 0, errEOF
	}
	for _, f := range [2]byte{flagName, flagComment} {
		if flg&f == 0 {
			continue
		}
		field := b[i:min(len(b), i+maxHeaderString)]
		n := bytes.IndexByte(field, 0)
		if n < 0 {
			if len(field) == maxHeaderString {
				return 0, ErrHeader
			}
			return 0, errEOF
		}
		i += n + 1
	}
	if flg&flagHCRC != 0 {
		if len(b) < i+2 {
			return 0, errEOF
		}
		if uint16(crc32.ChecksumIEEE(b[:i])) != binary.LittleEndian.Uint16(b[i:]) {
			return 0, ErrHeader
		}
		i += 2
	}
	return i, nil
}

const (
	window      = 32 << 10
	streamChunk = 1 << 20
	streamInput = 256 << 10
)

// Reader decompresses a gzip stream, like compress/gzip.Reader, including
// concatenated members.
//
// Unlike Gunzip, Reader computes the checksum inline: handing each 1 MB chunk
// to another goroutine cost as much as it saved (measured on macOS arm64).
type Reader struct {
	d    decoder
	r    int // next unread byte in d.out
	crc  uint32
	size uint32
	err  error
}

// NewReader reads the first gzip header from src. It returns io.EOF when src
// is empty.
func NewReader(src io.Reader) (*Reader, error) {
	z := &Reader{}
	if err := z.Reset(src); err != nil {
		return nil, err
	}
	return z, nil
}

// buffers are the 1.3 MB a Reader decodes with. Close puts them back in
// bufferPool so a program that opens many small streams, such as a search
// over rotated logs, does not allocate them for each one.
type buffers struct {
	in, out []byte
}

var bufferPool sync.Pool

// Reset discards the state of z and reads the first gzip header from src, as
// NewReader does, keeping z's buffers.
func (z *Reader) Reset(src io.Reader) error {
	in, out := z.d.inbuf, z.d.out
	if in == nil {
		if b, ok := bufferPool.Get().(*buffers); ok {
			in, out = b.in, b.out
		} else {
			in = make([]byte, streamInput)
			// fastOutMargin past the end lets the fast loop fill a whole
			// chunk
			out = make([]byte, 0, window+streamChunk+fastOutMargin)
		}
	}
	*z = Reader{}
	z.d.src = src
	z.d.stream = true
	z.d.inbuf = in
	z.d.out = out[:0]
	return z.member()
}

func (z *Reader) Read(p []byte) (int, error) {
	for z.r == len(z.d.out) {
		if z.err != nil {
			return 0, z.err
		}
		z.fill()
	}
	n := copy(p, z.d.out[z.r:])
	z.r += n
	return n, nil
}

// Close returns z's buffers for reuse. It does not close the underlying
// reader, like compress/gzip. Reset makes z usable again.
func (z *Reader) Close() error {
	if z.d.inbuf != nil {
		bufferPool.Put(&buffers{in: z.d.inbuf, out: z.d.out[:0]})
	}
	z.d.inbuf, z.d.out, z.d.in, z.r = nil, nil, nil, 0
	z.err = errClosed
	return nil
}

// fill decodes the next chunk once every decoded byte has been read.
func (z *Reader) fill() {
	d := &z.d
	if len(d.out) > window {
		// keep the last 32 KB as match history
		shift := len(d.out) - window
		copy(d.out, d.out[shift:])
		d.out = d.out[:window]
		z.r -= shift
		d.base = max(d.base-shift, 0)
	}
	start := len(d.out)
	err := d.step()
	// checksum while the new bytes are still in cache
	z.crc = crcUpdate(z.crc, d.out[start:])
	z.size += uint32(len(d.out) - start)
	if err != nil {
		z.err = err
		return
	}
	if d.state != blockDone {
		return
	}
	var t [8]byte
	for i := range t {
		if err := d.need(8); err != nil {
			z.err = err
			return
		}
		t[i] = byte(d.take(8))
	}
	if binary.LittleEndian.Uint32(t[:]) != z.crc || binary.LittleEndian.Uint32(t[4:]) != z.size {
		z.err = ErrChecksum
		return
	}
	z.err = z.member()
}

// member starts the next gzip member. It returns io.EOF at the end of input.
func (z *Reader) member() error {
	d := &z.d
	if d.ip >= len(d.in) && !d.more() {
		if d.srcErr != nil && d.srcErr != io.EOF {
			return d.srcErr
		}
		return io.EOF
	}
	// digest is the CRC-32 of the header bytes read so far, for FHCRC
	var digest uint32
	next := func() (byte, error) {
		if err := d.need(8); err != nil {
			return 0, err
		}
		b := byte(d.take(8))
		digest = crc32.Update(digest, crc32.IEEETable, []byte{b})
		return b, nil
	}
	skip := func(n int) error {
		for ; n > 0; n-- {
			if _, err := next(); err != nil {
				return err
			}
		}
		return nil
	}
	var h [10]byte
	for i := range h {
		b, err := next()
		if err != nil {
			return err
		}
		h[i] = b
	}
	if h[0] != 0x1f || h[1] != 0x8b || h[2] != 8 {
		return ErrHeader
	}
	flg := h[3]
	if flg&flagExtra != 0 {
		lo, err := next()
		if err != nil {
			return err
		}
		hi, err := next()
		if err != nil {
			return err
		}
		if err := skip(int(lo) | int(hi)<<8); err != nil {
			return err
		}
	}
	for _, f := range [2]byte{flagName, flagComment} {
		if flg&f == 0 {
			continue
		}
		for n := 0; ; n++ {
			if n == maxHeaderString {
				return ErrHeader
			}
			b, err := next()
			if err != nil {
				return err
			}
			if b == 0 {
				break
			}
		}
	}
	if flg&flagHCRC != 0 {
		want := uint16(digest)
		lo, err := next()
		if err != nil {
			return err
		}
		hi, err := next()
		if err != nil {
			return err
		}
		if uint16(lo)|uint16(hi)<<8 != want {
			return ErrHeader
		}
	}
	d.state = blockHeader
	d.final = false
	d.base = len(d.out)
	z.crc, z.size = 0, 0
	return nil
}
