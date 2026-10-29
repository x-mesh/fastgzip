package fastgzip

import (
	"errors"
	"io"
)

var (
	// ErrCorrupt reports DEFLATE data that does not decode.
	ErrCorrupt = errors.New("fastgzip: corrupt deflate data")
	errEOF     = io.ErrUnexpectedEOF
	errNoRoom  = errors.New("fastgzip: table too small")
)

// Root table sizes. Larger roots send fewer codes to sub-tables. The decoder
// tries 10/7 and falls back to 9/6 when the tables do not fit; zlib's ENOUGH
// bounds guarantee that 9/6 fits.
const (
	rootLenBits      = 10
	rootDistBits     = 7
	fallbackLenBits  = 9
	fallbackDistBits = 6
	lenTableSize     = 1444 // zlib ENOUGH: the 10/7 try shares this space
	distTableSize    = 592  // zlib ENOUGH_DISTS for 6 root bits
)

type blockState uint8

const (
	blockHeader blockState = iota
	blockStored
	blockHuffman
	blockDone
)

type decoder struct {
	in   []byte
	ip   int
	hold uint64
	bits uint

	// streaming input; src is nil when all input is in memory
	src    io.Reader
	srcErr error
	inbuf  []byte

	out  []byte
	base int // start of this stream's output in out; matches may not reach before it
	// stream stops decoding when out is full instead of growing it
	stream bool

	state      blockState
	final      bool
	storedLeft int
	lt, dt     []uint32
	lmask      uint64
	dmask      uint64

	blocks chan<- []byte
	sent   int

	lens  [320]uint16
	work  [288]uint16
	codes [lenTableSize + distTableSize]uint32
}

var fixedLit, fixedDist []uint32

func init() {
	var lens [288]uint16
	var work [288]uint16
	for i := range lens {
		switch {
		case i < 144:
			lens[i] = 8
		case i < 256:
			lens[i] = 9
		case i < 280:
			lens[i] = 7
		default:
			lens[i] = 8
		}
	}
	fixedLit = make([]uint32, 512)
	if _, _, err := buildTable(codeLitLen, lens[:], fixedLit, 9, work[:]); err != nil {
		panic(err)
	}
	for i := 0; i < 32; i++ {
		lens[i] = 5
	}
	fixedDist = make([]uint32, 32)
	if _, _, err := buildTable(codeDist, lens[:32], fixedDist, 5, work[:]); err != nil {
		panic(err)
	}
}

// Inflate decodes one raw DEFLATE stream from src and appends it to dst.
// It returns the extended slice and the number of input bytes consumed.
func Inflate(dst, src []byte) ([]byte, int, error) {
	return inflate(dst, src, nil)
}

// inflate sends finished blocks' output to blocks when it is not nil, at
// least crcBatch bytes at a time and the rest after the final block.
// Those bytes are never written again, so a reader may use them while
// decoding continues.
func inflate(dst, src []byte, blocks chan<- []byte) ([]byte, int, error) {
	d := decoder{in: src, out: dst, base: len(dst), blocks: blocks, sent: len(dst)}
	err := d.step()
	return d.out, d.ip, err
}

// more reads the next input chunk from src, keeping the unread bytes and the
// 8 bytes before them: the bit buffer may hand up to 7 of those back.
func (d *decoder) more() bool {
	if d.src == nil || d.srcErr != nil {
		return false
	}
	keep := min(d.ip, 8)
	n := copy(d.inbuf, d.in[d.ip-keep:])
	d.ip = keep
	m, err := io.ReadFull(d.src, d.inbuf[n:])
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	if err != nil {
		d.srcErr = err
	}
	d.in = d.inbuf[:n+m]
	return m > 0
}

// inputErr is the error for running out of input.
func (d *decoder) inputErr() error {
	if d.srcErr != nil && d.srcErr != io.EOF {
		return d.srcErr
	}
	return errEOF
}

func (d *decoder) pullByte() error {
	if d.ip >= len(d.in) && !d.more() {
		return d.inputErr()
	}
	d.hold |= uint64(d.in[d.ip]) << d.bits
	d.ip++
	d.bits += 8
	return nil
}

func (d *decoder) need(n uint) error {
	for d.bits < n {
		if err := d.pullByte(); err != nil {
			return err
		}
	}
	return nil
}

func (d *decoder) take(n uint) uint32 {
	v := uint32(d.hold & (1<<n - 1))
	d.hold >>= n
	d.bits -= n
	return v
}

// room is the output space left; unlimited when out can grow.
func (d *decoder) room() int {
	if !d.stream {
		return int(^uint(0) >> 1)
	}
	return cap(d.out) - len(d.out)
}

// step decodes blocks until the stream ends or, in stream mode, until out
// has no room for another symbol.
func (d *decoder) step() error {
	for {
		switch d.state {
		case blockHeader:
			if err := d.need(3); err != nil {
				return err
			}
			d.final = d.take(1) == 1
			var err error
			switch d.take(2) {
			case 0:
				err = d.storedHeader()
			case 1:
				d.lt, d.dt = fixedLit, fixedDist
				d.lmask, d.dmask = 1<<9-1, 1<<5-1
				d.state = blockHuffman
			case 2:
				err = d.dynamic()
			default:
				err = ErrCorrupt
			}
			if err != nil {
				return err
			}
		case blockStored:
			done, err := d.stored()
			if err != nil || !done {
				return err
			}
			d.endBlock()
		case blockHuffman:
			done, err := d.huffman()
			if err != nil || !done {
				return err
			}
			d.endBlock()
		case blockDone:
			return nil
		}
	}
}

func (d *decoder) endBlock() {
	if d.blocks != nil && len(d.out) > d.sent && (d.final || len(d.out)-d.sent >= crcBatch) {
		d.blocks <- d.out[d.sent:]
		d.sent = len(d.out)
	}
	if !d.final {
		d.state = blockHeader
		return
	}
	// give back whole bytes still in the bit buffer
	d.ip -= int(d.bits >> 3)
	d.hold, d.bits = 0, 0
	d.state = blockDone
}

func (d *decoder) storedHeader() error {
	d.take(d.bits & 7)
	if err := d.need(32); err != nil {
		return err
	}
	n := d.take(16)
	if n != d.take(16)^0xffff {
		return ErrCorrupt
	}
	d.storedLeft = int(n)
	d.state = blockStored
	return nil
}

// stored copies a stored block; done is false when out is full.
func (d *decoder) stored() (done bool, err error) {
	for d.storedLeft > 0 {
		if d.room() == 0 {
			return false, nil
		}
		if d.bits >= 8 {
			d.out = append(d.out, byte(d.take(8)))
			d.storedLeft--
			continue
		}
		if d.ip >= len(d.in) && !d.more() {
			return false, d.inputErr()
		}
		n := min(d.storedLeft, len(d.in)-d.ip, d.room())
		d.out = append(d.out, d.in[d.ip:d.ip+n]...)
		d.ip += n
		d.storedLeft -= n
	}
	return true, nil
}

var clOrder = [19]uint8{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}

func (d *decoder) dynamic() error {
	if err := d.need(14); err != nil {
		return err
	}
	nlen := int(d.take(5)) + 257
	ndist := int(d.take(5)) + 1
	ncode := int(d.take(4)) + 4
	if nlen > 286 || ndist > 30 {
		return ErrCorrupt
	}

	var cl [19]uint16
	for i := 0; i < ncode; i++ {
		if err := d.need(3); err != nil {
			return err
		}
		cl[clOrder[i]] = uint16(d.take(3))
	}
	var clTable [128]uint32
	_, clBits, err := buildTable(codeLens, cl[:], clTable[:], 7, d.work[:])
	if err != nil {
		return ErrCorrupt
	}
	clMask := uint64(1)<<clBits - 1

	lens := d.lens[:nlen+ndist]
	for i := 0; i < len(lens); {
		var e uint32
		for {
			e = clTable[d.hold&clMask]
			if entryBits(e) <= d.bits {
				break
			}
			if err := d.pullByte(); err != nil {
				return err
			}
		}
		d.take(entryBits(e))
		sym := entryVal(e)
		if sym < 16 {
			lens[i] = uint16(sym)
			i++
			continue
		}
		var rep int
		var val uint16
		switch sym {
		case 16:
			if i == 0 {
				return ErrCorrupt
			}
			if err := d.need(2); err != nil {
				return err
			}
			val, rep = lens[i-1], 3+int(d.take(2))
		case 17:
			if err := d.need(3); err != nil {
				return err
			}
			rep = 3 + int(d.take(3))
		default:
			if err := d.need(7); err != nil {
				return err
			}
			rep = 11 + int(d.take(7))
		}
		if i+rep > len(lens) {
			return ErrCorrupt
		}
		for ; rep > 0; rep-- {
			lens[i] = val
			i++
		}
	}
	if lens[256] == 0 {
		return ErrCorrupt
	}

	lb, db := uint(rootLenBits), uint(rootDistBits)
	for {
		used, lbits, err := buildTable(codeLitLen, lens[:nlen], d.codes[:], lb, d.work[:])
		if err != nil {
			return ErrCorrupt
		}
		_, dbits, err := buildTable(codeDist, lens[nlen:], d.codes[used:], db, d.work[:])
		if err == nil {
			d.lt, d.dt = d.codes[:used], d.codes[used:]
			d.lmask, d.dmask = uint64(1)<<lbits-1, uint64(1)<<dbits-1
			d.state = blockHuffman
			return nil
		}
		if err != errNoRoom || (lbits <= fallbackLenBits && dbits < rootDistBits) {
			return ErrCorrupt
		}
		lb, db = fallbackLenBits, fallbackDistBits
	}
}

// slowEntry decodes one table entry with byte-at-a-time refills. It is used
// near the ends of the input and output, where the fast loop's wide loads and
// stores would run past the buffers. pre is the bit buffer just before the
// entry's own bits, for extraBits.
func (d *decoder) slowEntry(table []uint32, mask uint64) (e uint32, pre uint64, err error) {
	for {
		e = table[d.hold&mask]
		op := entryOp(e)
		if op != 0 && op&0xf0 == 0 {
			root := entryBits(e)
			sub := table[entryVal(e)+uint32(d.hold>>root&(1<<op-1))]
			if root+entryBits(sub) <= d.bits {
				d.take(root)
				pre = d.hold
				d.take(entryBits(sub))
				return sub, pre, nil
			}
		} else if entryBits(e) <= d.bits {
			pre = d.hold
			d.take(entryBits(e))
			return e, pre, nil
		}
		if err := d.pullByte(); err != nil {
			return 0, 0, err
		}
	}
}

func extraBits(pre uint64, e uint32) uint32 {
	n := uint(entryOp(e) & 15)
	// & 63 lets the compiler drop its shift-by-64 guard; entryBits(e) <= 28
	return uint32(pre>>((entryBits(e)-n)&63)) & (1<<n - 1)
}

// huffman decodes the current compressed block. done is false when out has
// no room for another symbol (stream mode only).
func (d *decoder) huffman() (done bool, err error) {
	for {
		if len(d.in)-d.ip < fastInMargin {
			d.more()
		}
		if cap(d.out)-len(d.out) < fastOutMargin {
			if d.stream {
				return false, nil
			}
			d.grow()
		}
		if len(d.in)-d.ip >= fastInMargin {
			done, err := d.fast(d.lt, d.dt, d.lmask, d.dmask)
			if err != nil || done {
				return done, err
			}
			continue
		}

		// near the end of the input: one symbol at a time; the room check
		// above leaves space for a whole match
		e, pre, err := d.slowEntry(d.lt, d.lmask)
		if err != nil {
			return false, err
		}
		op := entryOp(e)
		switch {
		case op == opLiteral:
			d.out = append(d.out, byte(entryVal(e)))
			continue
		case op&opEnd != 0:
			return true, nil
		case op&opInvalid != 0:
			return false, ErrCorrupt
		}
		length := int(entryVal(e) + extraBits(pre, e))

		e, pre, err = d.slowEntry(d.dt, d.dmask)
		if err != nil {
			return false, err
		}
		if entryOp(e)&opBase == 0 {
			return false, ErrCorrupt
		}
		dist := int(entryVal(e) + extraBits(pre, e))
		if dist > len(d.out)-d.base {
			return false, ErrCorrupt
		}
		for i := 0; i < length; i++ {
			d.out = append(d.out, d.out[len(d.out)-dist])
		}
	}
}

// grow keeps room for the fast loop's over-writes.
func (d *decoder) grow() {
	n := 2*cap(d.out) + fastOutMargin
	out := make([]byte, len(d.out), n)
	copy(out, d.out)
	d.out = out
}
