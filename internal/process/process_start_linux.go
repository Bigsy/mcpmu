//go:build linux

package process

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func getProcessStartTicksPlatform(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	content := string(data)
	lastParen := strings.LastIndex(content, ")")
	if lastParen == -1 {
		return 0, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(content[lastParen+1:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("not enough fields in /proc/%d/stat", pid)
	}
	startTime, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse starttime: %w", err)
	}
	return startTime, nil
}
