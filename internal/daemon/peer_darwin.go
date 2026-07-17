//go:build darwin

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
	var cred *unix.Xucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		cred, socketErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return err
	}
	if socketErr != nil {
		return socketErr
	}
	if cred == nil || int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("peer UID does not match daemon UID %d", os.Getuid())
	}
	return nil
}
