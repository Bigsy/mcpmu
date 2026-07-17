//go:build windows

package shim

import (
	"context"
	"fmt"
)

type fileLock struct{}

func acquireLock(context.Context, string) (*fileLock, error) {
	return nil, fmt.Errorf("shared daemon transport is unsupported on windows")
}

func tryAcquireLock(string) (*fileLock, error) {
	return nil, fmt.Errorf("shared daemon transport is unsupported on windows")
}

func (*fileLock) Close() {}
