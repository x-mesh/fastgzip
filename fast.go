package fastgzip

import (
	"encoding/binary"
	"unsafe"
)

const (
	// Each fast iteration reads at most 8 bytes past the next input byte and
	// advances at most 16 bytes, so 25 bytes of input keep loads in bounds.
	fastInMargin = 25
	// One iteration writes up to 3 literals or a 258-byte match rounded up
	// to 16 bytes, so 323 bytes of spare capacity keep stores in bounds.
	fastOutMargin = 323
)

// stride8[d] is the smallest multiple of d that is at least 8: copying 8
// bytes from that far back repeats a period-d pattern without overlap.
var stride8 = [8]int{0, 8, 8, 9, 8, 10, 12, 14}

// stride16[d] is the smallest multiple of d that is at least 16, for 16-byte
// steps.
var stride16 = [16]int{0, 16, 16, 18, 16, 20, 18, 21, 16, 18, 20, 22, 24, 26, 28, 30}

// The fast loop indexes through unsafe pointers: per-access bounds checks
// cost about 30 instructions per 16-byte copy step. Every access stays in
// bounds because the loop runs only while fastInMargin input bytes and
// fastOutMargin output bytes remain, table indexes are masked to the table
// size, and dist is checked against the output written so far.
func load64(p unsafe.Pointer, off int) uint64 {
	return binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(p, off))[:])
}

func store64(p unsafe.Pointer, off int, v uint64) {
	binary.LittleEndian.PutUint64((*[8]byte)(unsafe.Add(p, off))[:], v)
}

func entryAt(t unsafe.Pointer, i uint64) uint32 {
	return *(*uint32)(unsafe.Add(t, i*4))
}

// fast decodes symbols until the end of the block or until the input or
// output margins run out. done reports the end of the block.
func (d *decoder) fast(lt, dt []uint32, lmask, dmask uint64) (done bool, err error) {
	inp := unsafe.Pointer(unsafe.SliceData(d.in))
	ip := d.ip
	last := len(d.in) - fastInMargin
	outp := unsafe.Pointer(unsafe.SliceData(d.out))
	o := len(d.out)
	end := cap(d.out) - fastOutMargin
	base := d.base
	ltp := unsafe.Pointer(unsafe.SliceData(lt))
	dtp := unsafe.Pointer(unsafe.SliceData(dt))

	bits := d.bits
	hold := d.hold & (1<<bits - 1)
	hold |= load64(inp, ip) << (bits & 63)
	ip += int(7 - bits>>3)
	bits |= 56
	// Like libdeflate, consuming an entry subtracts the whole entry from bits:
	// only the low 6 bits count, and they never borrow because the buffer
	// always holds at least the bits being consumed. This saves extracting
	// the length byte on every symbol.

	var (
		e, le    uint32
		pre, lpr uint64
	)
	// invariant: e is looked up from at least lenbits valid bits and not
	// yet consumed
	e = entryAt(ltp, hold&lmask)

	for {
		// the table load above overlaps this refill
		hold |= load64(inp, ip) << (bits & 63)
		ip += int((63 - bits&63) >> 3)
		bits |= 56
		pre = hold
		hold >>= e & 63
		bits -= uint(e)

	dispatch:
		// 3 literals (45 bits), or 2 literals and a length (50 bits), fit
		// in the 56 bits after a refill
		if e&0xff00 == 0 {
			*(*byte)(unsafe.Add(outp, o)) = byte(e >> 16)
			o++
			e = entryAt(ltp, hold&lmask)
			pre = hold
			hold >>= e & 63
			bits -= uint(e)
			if e&0xff00 == 0 {
				*(*byte)(unsafe.Add(outp, o)) = byte(e >> 16)
				o++
				e = entryAt(ltp, hold&lmask)
				pre = hold
				hold >>= e & 63
				bits -= uint(e)
				if e&0xff00 == 0 {
					*(*byte)(unsafe.Add(outp, o)) = byte(e >> 16)
					o++
					goto check
				}
			}
		}
		if e&(opBase<<8) == 0 {
			if e&(opInvalid<<8) != 0 {
				goto special
			}
			e = entryAt(ltp, uint64(entryVal(e))+hold&(1<<(entryOp(e)&15)-1))
			pre = hold
			hold >>= e & 63
			bits -= uint(e)
			if e&0xff00 == 0 {
				*(*byte)(unsafe.Add(outp, o)) = byte(e >> 16)
				o++
				goto check
			}
			if e&(opBase<<8) == 0 {
				goto special
			}
		}
		le, lpr = e, pre

		// at most 50 bits consumed since the last refill, so the buffer
		// still holds distbits real bits: look up first, then refill
		e = entryAt(dtp, hold&dmask)
		if bits&63 < 28 {
			hold |= load64(inp, ip) << (bits & 63)
			ip += int((63 - bits&63) >> 3)
			bits |= 56
		}
		pre = hold
		hold >>= e & 63
		bits -= uint(e)
		if e&(opBase<<8) == 0 {
			if e&(opInvalid<<8) != 0 {
				return false, ErrCorrupt
			}
			e = entryAt(dtp, uint64(entryVal(e))+hold&(1<<(entryOp(e)&15)-1))
			pre = hold
			hold >>= e & 63
			bits -= uint(e)
			if e&(opBase<<8) == 0 {
				return false, ErrCorrupt
			}
		}
		{
			dist := int(entryVal(e) + extraBits(pre, e))
			length := int(entryVal(le) + extraBits(lpr, le))

			// decode ahead: start the next symbol before the copy
			if bits&63 < 10 {
				hold |= load64(inp, ip) << (bits & 63)
				ip += int((63 - bits&63) >> 3)
				bits |= 56
			}
			e = entryAt(ltp, hold&lmask)
			hold |= load64(inp, ip) << (bits & 63)
			ip += int((63 - bits&63) >> 3)
			bits |= 56
			pre = hold
			hold >>= e & 63
			bits -= uint(e)

			if dist > o-base {
				return false, ErrCorrupt
			}
			s := o - dist
			// copies may run up to 15 bytes past o+length; later output
			// overwrites them and fastOutMargin keeps them in bounds
			if dist >= 16 {
				// most matches in log data are short: copy the first 32
				// bytes without a loop test
				a := load64(outp, s)
				b := load64(outp, s+8)
				store64(outp, o, a)
				store64(outp, o+8, b)
				a = load64(outp, s+16)
				b = load64(outp, s+24)
				store64(outp, o+16, a)
				store64(outp, o+24, b)
				for i := 32; i < length; i += 16 {
					a := load64(outp, s+i)
					b := load64(outp, s+i+8)
					store64(outp, o+i, a)
					store64(outp, o+i+8, b)
				}
			} else {
				// first 16 bytes: 8 at a time, each reading only finished bytes
				if dist >= 8 {
					store64(outp, o, load64(outp, s))
					store64(outp, o+8, load64(outp, s+8))
				} else {
					// repeat the last dist bytes into one 8-byte pattern
					v := load64(outp, s) & (1<<(8*uint(dist)) - 1)
					for sh := 8 * uint(dist); sh < 64; sh *= 2 {
						v |= v << sh
					}
					store64(outp, o, v)
					store64(outp, o+8, load64(outp, o+8-stride8[dist]))
				}
				// then 16 at a time from a multiple of dist at least 16 back
				st := stride16[dist]
				for i := 16; i < length; i += 16 {
					a := load64(outp, o+i-st)
					b := load64(outp, o+i-st+8)
					store64(outp, o+i, a)
					store64(outp, o+i+8, b)
				}
			}
			o += length
		}
		if ip > last || o > end {
			// undo the decode-ahead
			hold = pre
			bits += uint(e)
			break
		}
		goto dispatch

	check:
		if ip > last || o > end {
			break
		}
		e = entryAt(ltp, hold&lmask)
		continue

	special:
		if e&(opEnd<<8) != 0 {
			done = true
			break
		}
		return false, ErrCorrupt
	}

	bits &= 63
	n := bits >> 3
	ip -= int(n)
	bits &= 7
	d.ip = ip
	d.hold = hold & (1<<bits - 1)
	d.bits = bits
	d.out = d.out[:o]
	return done, nil
}
