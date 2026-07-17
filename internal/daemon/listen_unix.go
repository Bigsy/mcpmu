//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
)

var umaskMu sync.Mutex

func listenUnix(path string) (*net.UnixListener, error) {
	umaskMu.Lock()
	oldMask := syscall.Umask(0077)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(oldMask)
	umaskMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("secure daemon socket: %w", err)
	}
	return listener, nil
}
