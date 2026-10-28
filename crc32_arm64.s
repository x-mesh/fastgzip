#include "textflag.h"

// func crc32x3(c *[3]uint32, p *byte, n int)
//
// Updates three non-inverted IEEE CRC-32 states over p[0:n], p[n:2n] and
// p[2n:3n]. n must be a positive multiple of 16. CRC32X has a latency of 3
// cycles but issues every cycle, so three independent chains run about 3
// times faster than the single chain in hash/crc32.
TEXT ·crc32x3(SB),NOSPLIT,$0-24
	MOVD	c+0(FP), R0
	MOVD	p+8(FP), R1
	MOVD	n+16(FP), R2
	MOVWU	0(R0), R3
	MOVWU	4(R0), R4
	MOVWU	8(R0), R5
	ADD	R2, R1, R6
	ADD	R2, R6, R7

loop:
	LDP.P	16(R1), (R8, R9)
	LDP.P	16(R6), (R10, R11)
	LDP.P	16(R7), (R12, R13)
	CRC32X	R8, R3
	CRC32X	R10, R4
	CRC32X	R12, R5
	CRC32X	R9, R3
	CRC32X	R11, R4
	CRC32X	R13, R5
	SUBS	$16, R2, R2
	BNE	loop

	MOVW	R3, 0(R0)
	MOVW	R4, 4(R0)
	MOVW	R5, 8(R0)
	RET
