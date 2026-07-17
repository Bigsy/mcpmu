//go:build windows

package shim

import "fmt"

func spawnDetached(string, []string, string) error {
	return fmt.Errorf("shared daemon transport is unsupported on windows")
}
