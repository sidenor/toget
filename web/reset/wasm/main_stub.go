//go:build !(js && wasm)

// Command toget-reset-wasm is only built for the browser. This stub keeps
// `go build ./...` working on server and desktop platforms.
package main

func main() {}
