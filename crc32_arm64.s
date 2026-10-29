#include "textflag.h"

// FOLD advances the 128-bit remainder x by the distance in V24 and adds the
// data block d. t is scratch.
#define FOLD(x, t, d) \
	VPMULL	V24.D1, x.D1, t.Q1; \
	VPMULL2	V24.D2, x.D2, x.Q1; \
	VEOR	t.B16, x.B16, x.B16; \
	VEOR	d.B16, x.B16, x.B16

// func crc32pmull(crc uint32, p *byte, n int) uint32
//
// Returns the non-inverted IEEE CRC-32 state after p[0:n], from the state
// crc. n must be a positive multiple of 128. Eight 128-bit remainders fold
// forward by 1024 bits per iteration with carry-less multiplies, which keeps
// the SIMD units busy where CRC32X is limited to one 8-byte step per cycle.
//
// A fold constant for a distance of D bits is x^(D+32) mod P for the low
// half and x^(D-32) mod P for the high half, bit-reflected and shifted left
// by one.
TEXT ·crc32pmull(SB),NOSPLIT,$0-28
	MOVWU	crc+0(FP), R0
	MOVD	p+8(FP), R1
	MOVD	n+16(FP), R2

	VLD1.P	64(R1), [V0.B16, V1.B16, V2.B16, V3.B16]
	VLD1.P	64(R1), [V4.B16, V5.B16, V6.B16, V7.B16]
	VEOR	V16.B16, V16.B16, V16.B16
	VMOV	R0, V16.S[0]
	VEOR	V16.B16, V0.B16, V0.B16
	SUBS	$128, R2, R2
	BEQ	reduce

	MOVD	$0x1e88ef372, R3 // D = 1024
	MOVD	$0x14a7fe880, R4
	VMOV	R3, V24.D[0]
	VMOV	R4, V24.D[1]

loop:
	VLD1.P	64(R1), [V16.B16, V17.B16, V18.B16, V19.B16]
	VLD1.P	64(R1), [V20.B16, V21.B16, V22.B16, V23.B16]
	FOLD(V0, V8, V16)
	FOLD(V1, V9, V17)
	FOLD(V2, V10, V18)
	FOLD(V3, V11, V19)
	FOLD(V4, V12, V20)
	FOLD(V5, V13, V21)
	FOLD(V6, V14, V22)
	FOLD(V7, V15, V23)
	SUBS	$128, R2, R2
	BNE	loop

reduce:
	MOVD	$0x1751997d0, R3 // D = 128
	MOVD	$0xccaa009e, R4
	VMOV	R3, V24.D[0]
	VMOV	R4, V24.D[1]
	FOLD(V0, V8, V1)
	FOLD(V0, V8, V2)
	FOLD(V0, V8, V3)
	FOLD(V0, V8, V4)
	FOLD(V0, V8, V5)
	FOLD(V0, V8, V6)
	FOLD(V0, V8, V7)

	// V0 has the same CRC as the whole input, so the CRC of its 16 bytes
	// from a zero state is the result, without a Barrett reduction.
	VMOV	V0.D[0], R3
	VMOV	V0.D[1], R4
	MOVW	ZR, R0
	CRC32X	R3, R0
	CRC32X	R4, R0
	MOVW	R0, ret+24(FP)
	RET
