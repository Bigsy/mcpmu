//go:build unix

package main

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// interruptibleStdout gives the embedded session a pollable handle to an
// inherited pipe or socket. Close on a blocking os.File cannot wake a
// goroutine stuck in the kernel writing to a client that stopped reading.
func interruptibleStdout(file *os.File) (*os.File, func(), error) {
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 || file.SetDeadline(time.Time{}) == nil {
		return file, func() {}, nil
	}

	original := file.Fd()
	flags, err := unix.FcntlInt(original, unix.F_GETFL, 0)
	if err != nil {
		return nil, nil, err
	}
	fd, err := unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, nil, err
	}
	// NewFile registers an already nonblocking descriptor with Go's poller.
	// dup shares file status flags, so restore the inherited mode on exit.
	stream := os.NewFile(uintptr(fd), file.Name())
	cleanup := func() {
		_ = stream.Close()
		if flags&unix.O_NONBLOCK == 0 {
			_ = unix.SetNonblock(int(original), false)
		}
	}
	if err := stream.SetDeadline(time.Time{}); err != nil {
		cleanup()
		return nil, nil, err
	}
	return stream, cleanup, nil
}
