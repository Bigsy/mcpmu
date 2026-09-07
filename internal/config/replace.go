package config

import "encoding/json"

// Replace replaces persistent state and queues cache invalidations for removed
// or changed servers. It does not infer renames. Incoming state is copied, and
// its private pending operations are ignored. Errors leave the receiver intact.
func (c *Config) Replace(incoming *Config) error {
	if incoming == nil {
		return Errorf(ErrInvalidInput, "config cannot be nil")
	}
	if err := incoming.Validate(); err != nil {
		return Errorf(ErrInvalidInput, "invalid config: %w", err)
	}
	// Round-trip persistent fields to avoid aliasing the caller's maps, slices,
	// and optional values, while excluding private cache operations.
	data, err := json.Marshal(incoming)
	if err != nil {
		return err
	}
	var replacement Config
	if err := json.Unmarshal(data, &replacement); err != nil {
		return err
	}
	replacement.normalizeLoaded()
	replacement.toolCacheOps = append([]toolCacheOp(nil), c.toolCacheOps...)
	for name, old := range c.Servers {
		updated, ok := replacement.Servers[name]
		if !ok || toolSetChanged(old, updated) {
			replacement.noteToolCacheDelete(name)
		}
	}
	*c = replacement
	return nil
}
