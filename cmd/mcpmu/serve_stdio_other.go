//go:build !unix

package main

import "os"

// interruptibleStdio uses the platform's native standard stream.
func interruptibleStdio(file *os.File) (*os.File, func(), error) {
	return file, func() {}, nil
}
