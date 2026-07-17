//go:build linux

package daemon

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func validatePeerUID(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		cred, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if socketErr != nil {
		return socketErr
	}
	if cred == nil || int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("peer UID %d does not match daemon UID %d", cred.Uid, os.Getuid())
	}
	return nil
}
