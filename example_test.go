package fastgzip_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"

	"github.com/x-mesh/fastgzip"
)

func compressed(s string) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	return buf.Bytes()
}

func ExampleNewReader() {
	z, err := fastgzip.NewReader(bytes.NewReader(compressed("hello, gzip\n")))
	if err != nil {
		log.Fatal(err)
	}
	defer z.Close()
	text, err := io.ReadAll(z)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: hello, gzip
}

func ExampleGunzip() {
	text, err := fastgzip.Gunzip(compressed("whole file in memory\n"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(string(text))
	// Output: whole file in memory
}
