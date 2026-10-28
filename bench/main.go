// Command bench measures gzip decompression with the Go standard library,
// klauspost/compress, and fastgzip. Each file is decoded whole from memory,
// or with -stream through io.Reader from a 1 MB bufio.Reader. It checks that
// all decoders produce the same output and prints the median of -n runs.
//
//	go run . -stream -n 9 testdata/*.gz
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	kgzip "github.com/klauspost/compress/gzip"

	"github.com/x-mesh/fastgzip"
)

type decoder struct {
	name   string
	fn     func([]byte) ([]byte, error)
	reader func(io.Reader) (io.Reader, error)
}

// drain reads r to the end with 64 KB reads, as a line scanner would, and
// hashes the output when h is not nil.
func drain(r io.Reader, h io.Writer) error {
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		if h != nil {
			h.Write(buf[:n])
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readAll(r io.Reader, sizeHint int) ([]byte, error) {
	var buf bytes.Buffer
	buf.Grow(sizeHint)
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

func isize(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	n := len(b)
	return int(b[n-4]) | int(b[n-3])<<8 | int(b[n-2])<<16 | int(b[n-1])<<24
}

var decoders = []decoder{
	{"std", func(b []byte) ([]byte, error) {
		r, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return readAll(r, isize(b))
	}, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
	{"klauspost", func(b []byte) ([]byte, error) {
		r, err := kgzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		return readAll(r, isize(b))
	}, func(r io.Reader) (io.Reader, error) { return kgzip.NewReader(r) }},
	{"fastgzip", fastgzip.Gunzip, func(r io.Reader) (io.Reader, error) { return fastgzip.NewReader(r) }},
}

func main() {
	runs := flag.Int("n", 9, "runs per file and decoder")
	only := flag.String("only", "", "run only this decoder")
	stream := flag.Bool("stream", false, "decode through io.Reader from a 1 MB bufio.Reader, as edc grep does")
	flag.Parse()

	var files []string
	for _, a := range flag.Args() {
		m, _ := filepath.Glob(a)
		files = append(files, m...)
	}

	fmt.Printf("%-18s", "file")
	for _, d := range decoders {
		if *only == "" || *only == d.name {
			fmt.Printf("%12s", d.name)
		}
	}
	fmt.Println()

	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%-18s", filepath.Base(f))
		var want [32]byte
		for i, d := range decoders {
			if *only != "" && *only != d.name {
				continue
			}
			times := make([]time.Duration, *runs)
			var sum [32]byte
			for r := range times {
				t := time.Now()
				if *stream {
					var h hash.Hash
					if r == 0 {
						h = sha256.New()
					}
					var zr io.Reader
					zr, err = d.reader(bufio.NewReaderSize(bytes.NewReader(src), 1<<20))
					if err == nil {
						if h != nil {
							err = drain(zr, h)
							copy(sum[:], h.Sum(nil))
						} else {
							err = drain(zr, nil)
						}
					}
				} else {
					var out []byte
					out, err = d.fn(src)
					if r == 0 {
						sum = sha256.Sum256(out)
					}
				}
				times[r] = time.Since(t)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n%s %s: %v\n", d.name, f, err)
					os.Exit(1)
				}
			}
			if i == 0 || want == [32]byte{} {
				want = sum
			} else if sum != want {
				fmt.Fprintf(os.Stderr, "\n%s %s: output differs\n", d.name, f)
				os.Exit(1)
			}
			slices.Sort(times)
			fmt.Printf("%12.2f", float64(times[len(times)/2].Microseconds())/1000)
		}
		fmt.Println()
	}
}
