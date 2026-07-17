//go:build darwin

package process

import "golang.org/x/sys/unix"

func getProcessStartTicksPlatform(pid int) (int64, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	started := info.Proc.P_starttime
	return started.Sec*1_000_000_000 + int64(started.Usec)*1_000, nil
}
