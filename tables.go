// buildTable is a port of inftrees.c from zlib 1.2.12, altered to write
// entries in the layout described below. zlib is Copyright (C) 1995-2022
// Mark Adler; see NOTICE for its license.

package fastgzip

// A table entry packs one decoding step into a uint32:
//
//	bits 0-7   number of bits to consume: code length plus extra bits
//	bits 8-15  op: 0 literal, 16|n length/distance base with n extra bits,
//	           32 end of block, 64 invalid, 1-15 link to a sub-table of 2^op
//	bits 16-31 literal, base value, or sub-table offset
//
// Counting the extra bits in bits 0-7 lets the decoder consume a code and its
// extra bits with one shift.
const (
	opLiteral = 0
	opBase    = 16
	opEnd     = 32
	opInvalid = 64
)

func entryBits(e uint32) uint  { return uint(e & 0xff) }
func entryOp(e uint32) uint32  { return (e >> 8) & 0xff }
func entryVal(e uint32) uint32 { return e >> 16 }

func makeEntry(bits, op, val uint32) uint32 { return bits | op<<8 | val<<16 }

type codeType int

const (
	codeLens codeType = iota
	codeLitLen
	codeDist
)

const maxBits = 15

var (
	lenBase   = [31]uint16{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258, 0, 0}
	lenExtra  = [31]uint16{16, 16, 16, 16, 16, 16, 16, 16, 17, 17, 17, 17, 18, 18, 18, 18, 19, 19, 19, 19, 20, 20, 20, 20, 21, 21, 21, 21, 16, 64, 64}
	distBase  = [32]uint16{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577, 0, 0}
	distExtra = [32]uint16{16, 16, 16, 16, 17, 17, 18, 18, 19, 19, 20, 20, 21, 21, 22, 22, 23, 23, 24, 24, 25, 25, 26, 26, 27, 27, 28, 28, 29, 29, 64, 64}
)

// buildTable is a port of zlib 1.2.12 inflate_table. It writes the table for
// lens into table and returns the entries used and the root bits chosen.
// errNoRoom means table is too small for this root size; the caller retries
// with a smaller root.
func buildTable(typ codeType, lens []uint16, table []uint32, root uint, work []uint16) (used int, rootOut uint, err error) {
	var count, offs [maxBits + 1]uint16
	for _, l := range lens {
		count[l]++
	}

	max := uint(maxBits)
	for max >= 1 && count[max] == 0 {
		max--
	}
	if root > max {
		root = max
	}
	if max == 0 {
		// no codes: two invalid entries so decoding fails cleanly
		table[0] = makeEntry(1, opInvalid, 0)
		table[1] = makeEntry(1, opInvalid, 0)
		return 2, 1, nil
	}
	min := uint(1)
	for min < max && count[min] == 0 {
		min++
	}
	if root < min {
		root = min
	}

	left := 1
	for l := 1; l <= maxBits; l++ {
		left <<= 1
		left -= int(count[l])
		if left < 0 {
			return 0, 0, ErrCorrupt
		}
	}
	if left > 0 && (typ == codeLens || max != 1) {
		return 0, 0, ErrCorrupt
	}

	for l := 1; l < maxBits; l++ {
		offs[l+1] = offs[l] + count[l]
	}
	for sym, l := range lens {
		if l != 0 {
			work[offs[l]] = uint16(sym)
			offs[l]++
		}
	}

	var base, extra []uint16
	var match uint32
	switch typ {
	case codeLens:
		match = 20
	case codeLitLen:
		base, extra, match = lenBase[:], lenExtra[:], 257
	default:
		base, extra, match = distBase[:], distExtra[:], 0
	}

	var (
		huff uint32
		sym  int
		l    = min
		next = 0
		curr = root
		drop uint
		low  = ^uint32(0)
	)
	used = 1 << root
	mask := uint32(used - 1)
	if used > len(table) {
		return 0, 0, errNoRoom
	}

	for {
		var here uint32
		s := uint32(work[sym])
		switch {
		case s+1 < match:
			here = makeEntry(uint32(l-drop), opLiteral, s)
		case s >= match:
			op := uint32(extra[s-match])
			bits := uint32(l - drop)
			if op&opBase != 0 {
				bits += op & 15
			}
			here = makeEntry(bits, op, uint32(base[s-match]))
		default:
			here = makeEntry(uint32(l-drop), opEnd|opInvalid, 0)
		}

		incr := uint32(1) << (l - drop)
		fill := uint32(1) << curr
		minFill := fill
		for {
			fill -= incr
			table[next+int(huff>>drop+fill)] = here
			if fill == 0 {
				break
			}
		}

		incr = 1 << (l - 1)
		for huff&incr != 0 {
			incr >>= 1
		}
		if incr != 0 {
			huff &= incr - 1
			huff += incr
		} else {
			huff = 0
		}

		sym++
		count[l]--
		if count[l] == 0 {
			if l == max {
				break
			}
			l = uint(lens[work[sym]])
		}

		if l > root && huff&mask != low {
			if drop == 0 {
				drop = root
			}
			next += int(minFill)
			curr = l - drop
			left := 1 << curr
			for curr+drop < max {
				left -= int(count[curr+drop])
				if left <= 0 {
					break
				}
				curr++
				left <<= 1
			}
			used += 1 << curr
			if used > len(table) {
				return 0, 0, errNoRoom
			}
			low = huff & mask
			table[low] = makeEntry(uint32(root), uint32(curr), uint32(next))
		}
	}

	if huff != 0 {
		table[next+int(huff)] = makeEntry(uint32(l-drop), opInvalid, 0)
	}
	return used, root, nil
}
