# fastgzip

fastgzip decodes gzip and DEFLATE data in Go, without cgo. On an Apple M1 Pro, it decodes a 100 MB log about 3.3 times faster than `klauspost/compress/gzip`, at the speed of the system zlib on macOS.

## Install

```sh
go get github.com/x-mesh/fastgzip
```

fastgzip needs Go 1.24 or later. It has no dependencies.

## Use

`Reader` replaces `compress/gzip.Reader` for reads:

```go
z, err := fastgzip.NewReader(file)
if err != nil {
	return err
}
defer z.Close()
_, err = io.Copy(dst, z)
```

Call `Close` when you do not need the reader. `Close` gives the 1.3 MB of buffers back to a pool, and the next `NewReader` uses them again. `Reset` starts a new stream on the same reader.

If the data is already in memory, use `Gunzip`:

```go
text, err := fastgzip.Gunzip(compressed)
```

`Inflate(dst, src)` decodes one raw DEFLATE stream. It appends the result to `dst` and returns the number of input bytes it used.

The errors are `ErrHeader`, `ErrChecksum` and `ErrCorrupt`. Truncated input gives `io.ErrUnexpectedEOF`. Before `Reader` returns that error, it returns all the bytes that it decoded.

## Differences from compress/gzip

- `Reader` does not give the header fields (`Name`, `Comment`, `ModTime`).
- `Reader` always reads concatenated members as one stream. It has no `Multistream` switch.
- fastgzip does not compress data.

## Performance

The table shows the median time of 11 runs in milliseconds. The machine is an Apple M1 Pro with macOS 27 and Go 1.27.3. Each decoder reads through a 1 MB `bufio.Reader` with 64 KB reads.

| File | Size | compress/gzip | klauspost v1.20.3 | fastgzip |
|---|---|---|---|---|
| Application log, `gzip -1` | 100 MB | 357 | 241 | 70.5 |
| Binaries, level 6 | 16 MB | 64 | 44 | 17.6 |
| Word list, level 1 | 2.5 MB | 14.4 | 9.9 | 3.4 |
| Short repeated patterns, level 6 | 40 MB | 13.8 | 11.7 | 7.2 |
| Byte runs, `Z_RLE` | 15 MB | 8.1 | 7.0 | 3.0 |

For comparison, Apple's libz decodes the 100 MB log in 71.4 ms with one C `inflate` call. `Gunzip` decodes it in 68.7 ms. On Linux arm64 (Docker on the same Mac), the `Reader` time for the log is 70.9 ms. On the same machine, klauspost needs 244 ms, and Debian zlib 1.3.1 needs 193 ms through Python.

We did not measure performance on amd64.

To measure your own files, run the benchmark tool. It also makes sure that all the decoders give the same output.

```sh
cd bench
go run . -stream -n 9 /path/to/*.gz
```

## Why it is fast

- A 64-bit bit buffer. One unaligned 8-byte load refills it, with no branch.
- Each table entry holds the code length plus the extra bits. One shift removes a code and its extra bits.
- Root tables of 10 and 7 bits, so fewer codes need a second table lookup. If the tables do not fit, the decoder uses 9 and 6 bits.
- The table load for the next symbol starts before the refill, so the two delays overlap.
- The decoder reads the next symbol before it copies a match.
- The bit count uses the libdeflate method. The decoder subtracts the full entry and uses only the low 6 bits.
- Match copies move 16 bytes at a time. The first 32 bytes have no loop test, because most matches in log data are 16 bytes or shorter. A short distance repeats as a pattern.
- On arm64, an assembly function computes CRC-32 with `PMULL` carry-less multiplies. It folds eight 128-bit lanes in parallel. This is 45 GB/s, and `hash/crc32` is 8.4 GB/s.
- Above 1 MB, `Gunzip` computes the CRC-32 on a second goroutine. The decoder gives it the output in steps of 1 MB or more, because each step can wake the goroutine.

## Safety

The fast decode loop uses `unsafe` pointers and has no bounds checks. These limits keep each access in its buffer:

- The loop stops when fewer than 25 input bytes or 323 output bytes remain.
- Each table index has a mask with the table size.
- The decoder compares each match distance with the output that it wrote.

The tests compare the output with `compress/flate` and `compress/gzip` on random and fuzz input. They run with `-race` and with `-gcflags=all=-d=checkptr`. We ran them on darwin/arm64, linux/arm64 and linux/amd64.

## Tests

```sh
go test ./...
go test -run '^$' -fuzz FuzzReader -fuzztime 60s .
```

To compare a directory of `.gz` files with `compress/gzip`, set `FASTGZIP_CORPUS`:

```sh
FASTGZIP_CORPUS=/path/to/gz-files go test .
```

To profile one file, set `FASTGZIP_BENCH` and run `BenchmarkReader` or `BenchmarkGunzip`.

## License

MIT. The table builder in `tables.go` is a port of zlib 1.2.12. [NOTICE](NOTICE) has the zlib license.
