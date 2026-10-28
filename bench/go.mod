module github.com/x-mesh/fastgzip/bench

go 1.25

require (
	github.com/klauspost/compress v1.20.3
	github.com/x-mesh/fastgzip v0.0.0
)

// the benchmark always measures the fastgzip in this repository
replace github.com/x-mesh/fastgzip => ../
