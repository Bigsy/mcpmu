//go:build windows

package daemon

import "fmt"

func signalDaemon(int) error {
	return fmt.Errorf("shared daemon transport is unsupported on windows")
}
