//go:build !darwin && !linux

package daemon

import (
	"net"
)

func validatePeerUID(*net.UnixConn) error {
	// The private 0700 runtime directory and 0600 socket remain the access
	// control boundary on Unix platforms without a supported peer-credential
	// socket API.
	return nil
}
