//go:build !darwin && !freebsd && !linux

package process

import (
	"fmt"
	"runtime"
)

func getProcessStartTicksPlatform(_ int) (int64, error) {
	return 0, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
}
