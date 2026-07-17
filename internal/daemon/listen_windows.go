//go:build windows

package daemon

import (
	"fmt"
	"net"
)

func listenUnix(string) (*net.UnixListener, error) {
	return nil, fmt.Errorf("shared daemon transport is unsupported on windows")
}
