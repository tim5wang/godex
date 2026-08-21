//go:build !wasip1

// Host-platform stub. The real plugin source (main.go) only compiles for
// GOOS=wasip1 because of //go:wasmexport / //go:wasmimport; this stub keeps
// `go build ./...` and `go vet ./...` green on the host toolchain.
package main

func main() {}
