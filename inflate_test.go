package fastgzip

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
)

func stdGunzip(t testing.TB, b []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// FASTGZIP_CORPUS points at a directory of .gz files, such as the corpus
// built for the C comparison.
func TestCorpus(t *testing.T) {
	dir := os.Getenv("FASTGZIP_CORPUS")
	if dir == "" {
		t.Skip("FASTGZIP_CORPUS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.gz"))
	if len(files) == 0 {
		t.Fatal("no .gz files in", dir)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Gunzip(b)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if want := stdGunzip(t, b); !bytes.Equal(got, want) {
			t.Errorf("%s: output differs (%d vs %d bytes)", filepath.Base(f), len(got), len(want))
		}
	}
}

func sampleData(r *rand.Rand, n int) []byte {
	b := make([]byte, 0, n)
	for len(b) < n {
		switch r.IntN(4) {
		case 0: // random bytes
			for k := r.IntN(64); k > 0; k-- {
				b = append(b, byte(r.Uint32()))
			}
		case 1: // run of one byte
			c := byte(r.Uint32())
			for k := r.IntN(300); k > 0; k-- {
				b = append(b, c)
			}
		case 2: // short period
			p := 1 + r.IntN(20)
			unit := make([]byte, p)
			for i := range unit {
				unit[i] = byte('a' + r.IntN(26))
			}
			for k := r.IntN(30); k > 0; k-- {
				b = append(b, unit...)
			}
		default: // repeat from far back
			if len(b) > 0 {
				s := r.IntN(len(b))
				k := min(r.IntN(400), len(b)-s)
				b = append(b, b[s:s+k]...)
			}
		}
	}
	return b[:n]
}

func TestRandomRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	sizes := []int{0, 1, 2, 7, 25, 100, 323, 1000, 4096, 65536, 1 << 20}
	levels := []int{flate.NoCompression, flate.BestSpeed, 5, flate.BestCompression, flate.HuffmanOnly}
	for _, n := range sizes {
		for _, lv := range levels {
			for rep := 0; rep < 3; rep++ {
				data := sampleData(r, n)
				var buf bytes.Buffer
				w, _ := flate.NewWriter(&buf, lv)
				w.Write(data)
				w.Close()
				got, used, err := Inflate(nil, buf.Bytes())
				if err != nil {
					t.Fatalf("n=%d level=%d: %v", n, lv, err)
				}
				if !bytes.Equal(got, data) {
					t.Fatalf("n=%d level=%d: output differs", n, lv)
				}
				if used != buf.Len() {
					t.Fatalf("n=%d level=%d: consumed %d of %d", n, lv, used, buf.Len())
				}
			}
		}
	}
}

func TestPrefixAndHistory(t *testing.T) {
	// a match may not reach into data that was in dst before the stream
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, 9)
	w.Write(bytes.Repeat([]byte("abcdefgh"), 100))
	w.Close()
	prefix := []byte("unrelated prefix")
	got, _, err := Inflate(append([]byte(nil), prefix...), buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:len(prefix)], prefix) || !bytes.Equal(got[len(prefix):], bytes.Repeat([]byte("abcdefgh"), 100)) {
		t.Fatal("output differs")
	}
}

func TestTruncated(t *testing.T) {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, 6)
	w.Write(sampleData(rand.New(rand.NewPCG(3, 4)), 100000))
	w.Close()
	full := buf.Bytes()
	for _, n := range []int{0, 1, 10, len(full) / 2, len(full) - 1} {
		if _, _, err := Inflate(nil, full[:n]); err == nil {
			t.Errorf("truncated to %d bytes: no error", n)
		}
	}
}

func FuzzInflate(f *testing.F) {
	r := rand.New(rand.NewPCG(5, 6))
	for _, lv := range []int{1, 6, 9, flate.HuffmanOnly} {
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, lv)
		w.Write(sampleData(r, 5000))
		w.Close()
		f.Add(buf.Bytes())
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		got, _, err := Inflate(nil, b)
		want, werr := io.ReadAll(flate.NewReader(bytes.NewReader(b)))
		if err == nil && werr == nil && !bytes.Equal(got, want) {
			t.Fatal("output differs from compress/flate")
		}
		if err == nil && werr != nil {
			t.Fatalf("accepted input compress/flate rejects: %v", werr)
		}
	})
}

// FASTGZIP_BENCH names one .gz file to decode.
func BenchmarkGunzip(b *testing.B) {
	f := os.Getenv("FASTGZIP_BENCH")
	if f == "" {
		b.Skip("FASTGZIP_BENCH not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	out, err := Gunzip(src)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(out)))
	for b.Loop() {
		if _, err := Gunzip(src); err != nil {
			b.Fatal(err)
		}
	}
}

// readWith reads r to the end with reads of n bytes.
func readWith(r io.Reader, n int) ([]byte, error) {
	var out []byte
	buf := make([]byte, n)
	for {
		k, err := r.Read(buf)
		out = append(out, buf[:k]...)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

func gzipBytes(t testing.TB, data []byte, level int) []byte {
	var buf bytes.Buffer
	w, _ := gzip.NewWriterLevel(&buf, level)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

func TestReaderCorpus(t *testing.T) {
	dir := os.Getenv("FASTGZIP_CORPUS")
	if dir == "" {
		t.Skip("FASTGZIP_CORPUS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.gz"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		want := stdGunzip(t, b)
		sources := map[string]func() io.Reader{
			"bytes": func() io.Reader { return bytes.NewReader(b) },
			"half":  func() io.Reader { return iotest.HalfReader(bytes.NewReader(b)) },
		}
		if len(b) < 1<<20 {
			sources["onebyte"] = func() io.Reader { return iotest.OneByteReader(bytes.NewReader(b)) }
		}
		for name, src := range sources {
			for _, n := range []int{1, 7, 4096, 1 << 20} {
				if n == 1 && len(want) > 1<<22 {
					continue
				}
				z, err := NewReader(src())
				if err != nil {
					t.Fatalf("%s %s: %v", filepath.Base(f), name, err)
				}
				got, err := readWith(z, n)
				if err != nil {
					t.Fatalf("%s %s read %d: %v", filepath.Base(f), name, n, err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("%s %s read %d: output differs (%d vs %d bytes)", filepath.Base(f), name, n, len(got), len(want))
				}
			}
		}
	}
}

func TestReaderMembers(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	var all, want []byte
	for _, n := range []int{100, 0, 3 << 20, 1, 70000} {
		data := sampleData(r, n)
		want = append(want, data...)
		all = append(all, gzipBytes(t, data, 6)...)
	}
	z, err := NewReader(iotest.HalfReader(bytes.NewReader(all)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := readWith(z, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs (%d vs %d bytes)", len(got), len(want))
	}
	if g, err := Gunzip(all); err != nil || !bytes.Equal(g, want) {
		t.Fatalf("Gunzip: err=%v equal=%v", err, bytes.Equal(g, want))
	}
}

func TestReaderErrors(t *testing.T) {
	if _, err := NewReader(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("empty input: %v, want io.EOF", err)
	}
	if _, err := NewReader(bytes.NewReader([]byte("not gzip data"))); err == nil {
		t.Fatal("bad header: no error")
	}

	full := gzipBytes(t, sampleData(rand.New(rand.NewPCG(9, 10)), 2<<20), 6)
	for _, n := range []int{11, 1000, len(full) / 2, len(full) - 9, len(full) - 1} {
		z, err := NewReader(bytes.NewReader(full[:n]))
		if err != nil {
			continue
		}
		if _, err := readWith(z, 4096); err == nil {
			t.Errorf("truncated to %d bytes: no error", n)
		}
	}

	bad := append([]byte(nil), full...)
	bad[len(bad)-6] ^= 1 // CRC-32 in the trailer
	z, _ := NewReader(bytes.NewReader(bad))
	if _, err := readWith(z, 4096); err != ErrChecksum {
		t.Errorf("bad checksum: %v", err)
	}

	sentinel := errors.New("disk error")
	z, _ = NewReader(io.MultiReader(bytes.NewReader(full[:len(full)/2]), iotest.ErrReader(sentinel)))
	if _, err := readWith(z, 4096); err != sentinel {
		t.Errorf("source error: %v, want %v", err, sentinel)
	}
}

func FuzzReader(f *testing.F) {
	r := rand.New(rand.NewPCG(11, 12))
	for _, lv := range []int{1, 6, 9} {
		f.Add(gzipBytes(f, sampleData(r, 3000), lv))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var got []byte
		z, err := NewReader(bytes.NewReader(b))
		if err == nil {
			got, err = readWith(z, 777)
		}
		sr, werr := gzip.NewReader(bytes.NewReader(b))
		var want []byte
		if werr == nil {
			want, werr = io.ReadAll(sr)
		}
		if err == nil && werr == nil && !bytes.Equal(got, want) {
			t.Fatal("output differs from compress/gzip")
		}
		if err == nil && werr != nil && werr != io.EOF {
			t.Fatalf("accepted input compress/gzip rejects: %v", werr)
		}
	})
}

// FASTGZIP_BENCH names one .gz file to decode through Reader, as edc grep
// does: from a 1 MB bufio.Reader, with 64 KB reads.
func BenchmarkReader(b *testing.B) {
	f := os.Getenv("FASTGZIP_BENCH")
	if f == "" {
		b.Skip("FASTGZIP_BENCH not set")
	}
	src, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(stdGunzip(b, src))))
	buf := make([]byte, 64<<10)
	for b.Loop() {
		z, err := NewReader(bufio.NewReaderSize(bytes.NewReader(src), 1<<20))
		if err != nil {
			b.Fatal(err)
		}
		for {
			_, err := z.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func TestReaderResetAndClose(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 16))
	var z *Reader
	for i, n := range []int{3 << 20, 10, 0, 500000, 2 << 20} {
		data := sampleData(r, n)
		src := gzipBytes(t, data, 1+i%9)
		var err error
		if z == nil {
			z, err = NewReader(bytes.NewReader(src))
		} else {
			err = z.Reset(bytes.NewReader(src))
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := readWith(z, 5000)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("stream %d: err=%v equal=%v", i, err, bytes.Equal(got, data))
		}
		if i%2 == 1 {
			// a closed Reader hands its buffers to the next NewReader
			z.Close()
			if _, err := z.Read(make([]byte, 1)); err == nil {
				t.Fatal("read after Close: no error")
			}
			z = nil
		}
	}
}

// edc shows a truncated HTTP body up to the point it was cut.
func TestReaderTruncatedPrefix(t *testing.T) {
	data := sampleData(rand.New(rand.NewPCG(17, 18)), 3<<20)
	full := gzipBytes(t, data, 6)
	z, err := NewReader(bytes.NewReader(full[:len(full)/2]))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(z)
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
	if len(got) < len(data)/4 || !bytes.Equal(got, data[:len(got)]) {
		t.Fatalf("got %d bytes; want a correct prefix of %d", len(got), len(data))
	}
}
