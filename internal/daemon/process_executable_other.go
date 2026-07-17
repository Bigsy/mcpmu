//go:build !darwin && !linux

package daemon

import (
	"fmt"
	"runtime"
)

func processExecutablePath(int) (string, error) {
	return "", fmt.Errorf("process executable validation is unsupported on %s", runtime.GOOS)
}
