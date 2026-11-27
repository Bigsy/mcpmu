package config

import (
	"encoding/json"
	"errors"
)

// RequestLimit bounds concurrent requests in one stdio session.
// Its zero value selects the default when the configuration field is absent.
type RequestLimit int

var errRequestLimit = errors.New("maxConcurrentRequestsPerSession must be an integer from 1 to 4096")

// Effective returns the configured limit, or 64 for the zero value.
func (l RequestLimit) Effective() (int, error) {
	if l == 0 {
		return 64, nil
	}
	if l < 1 || l > 4096 {
		return 0, errRequestLimit
	}
	return int(l), nil
}

// UnmarshalJSON accepts an explicit integer from 1 to 4096.
func (l *RequestLimit) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err != nil || n == 0 {
		return errRequestLimit
	}
	value := RequestLimit(n)
	if _, err := value.Effective(); err != nil {
		return err
	}
	*l = value
	return nil
}
