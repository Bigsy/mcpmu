package config

import (
	"fmt"
	"slices"
)

// PermissionChange is one incremental edit. Remove reverts to the default;
// otherwise Enabled selects an explicit allow or deny.
type PermissionChange struct {
	Server  string
	Tool    string
	Enabled bool
	Remove  bool
}

func (c *Config) validatePermission(namespace, server, tool string) error {
	if _, ok := c.Namespaces[namespace]; !ok {
		return Errorf(ErrNotFound, "namespace %q not found", namespace)
	}
	if _, ok := c.Servers[server]; !ok {
		return Errorf(ErrNotFound, "server %q not found", server)
	}
	if tool == "" {
		return Errorf(ErrInvalidInput, "tool name cannot be empty")
	}
	return nil
}

// ApplyPermissionChanges validates the entire batch before applying anything.
// Removal of an absent permission is idempotent, but its namespace and server
// must still exist. On failure even aliased permission slices stay unchanged.
func (c *Config) ApplyPermissionChanges(namespace string, changes []PermissionChange) error {
	if _, ok := c.Namespaces[namespace]; !ok {
		return Errorf(ErrNotFound, "namespace %q not found", namespace)
	}
	for _, change := range changes {
		if err := c.validatePermission(namespace, change.Server, change.Tool); err != nil {
			return fmt.Errorf("permission %s:%s: %w", change.Server, change.Tool, err)
		}
	}
	next := *c
	next.ToolPermissions = slices.Clone(c.ToolPermissions)
	for _, change := range changes {
		var err error
		if change.Remove {
			err = next.UnsetToolPermission(namespace, change.Server, change.Tool)
		} else {
			err = next.SetToolPermission(namespace, change.Server, change.Tool, change.Enabled)
		}
		if err != nil {
			return err
		}
	}
	c.ToolPermissions = next.ToolPermissions
	return nil
}

// ReplaceToolPermissions replaces just this namespace's list after validation.
// Order and duplicates are preserved, matching full-list API semantics.
func (c *Config) ReplaceToolPermissions(namespace string, permissions []ToolPermission) error {
	if _, ok := c.Namespaces[namespace]; !ok {
		return Errorf(ErrNotFound, "namespace %q not found", namespace)
	}
	for _, tp := range permissions {
		if err := c.validatePermission(namespace, tp.Server, tp.ToolName); err != nil {
			return err
		}
	}
	next := make([]ToolPermission, 0, len(c.ToolPermissions)+len(permissions))
	for _, tp := range c.ToolPermissions {
		if tp.Namespace != namespace {
			next = append(next, tp)
		}
	}
	for _, tp := range permissions {
		tp.Namespace = namespace
		next = append(next, tp)
	}
	c.ToolPermissions = next
	return nil
}
