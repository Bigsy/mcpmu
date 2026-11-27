//go:build !unix

package main

import "os"

// interruptibleStdout uses the platform's native standard stream.
func interruptibleStdout(file *os.File) (*os.File, func(), error) {
	return file, func() {}, nil
}
