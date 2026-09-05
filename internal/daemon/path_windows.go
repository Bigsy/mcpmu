//go:build windows

package daemon

import "fmt"

func ensurePrivateRuntimeDir(string) error {
	return fmt.Errorf("shared daemon transport is unsupported on windows")
}

func validatePrivateRuntimeDir(string) error {
	return fmt.Errorf("shared daemon transport is unsupported on windows")
}
