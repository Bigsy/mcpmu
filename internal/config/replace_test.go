package config

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestReplacePreservesOnlyReceiverCacheOperations(t *testing.T) {
	c := NewConfig()
	c.Servers["unchanged"] = ServerConfig{Command: "echo", Args: []string{"one"}}
	c.Servers["removed"] = ServerConfig{Command: "echo"}
	c.noteToolCacheRename("earlier", "unchanged")
	incoming := NewConfig()
	incoming.Servers["unchanged"] = ServerConfig{Command: "echo", Args: []string{"one"}}
	incoming.Servers["added"] = ServerConfig{Command: "echo"}
	incoming.noteToolCacheDelete("unchanged")
	if err := c.Replace(incoming); err != nil {
		t.Fatal(err)
	}
	want := []toolCacheOp{{oldName: "earlier", newName: "unchanged"}, {name: "removed"}}
	if !reflect.DeepEqual(c.toolCacheOps, want) {
		t.Fatalf("cache ops = %#v", c.toolCacheOps)
	}
	incoming.Servers["unchanged"].Args[0] = "caller edit"
	if c.Servers["unchanged"].Args[0] != "one" {
		t.Error("replacement aliases incoming state")
	}
}

func TestReplaceInvalidLeavesReceiverAndOperations(t *testing.T) {
	for _, incoming := range []*Config{nil, {Servers: map[string]ServerConfig{"invalid": {}}}} {
		c := NewConfig()
		c.Servers["keep"] = ServerConfig{Command: "echo"}
		c.noteToolCacheDelete("earlier")
		before := slices.Clone(c.toolCacheOps)
		if err := c.Replace(incoming); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error = %v", err)
		}
		if _, ok := c.Servers["keep"]; !ok || len(c.Servers) != 1 || !reflect.DeepEqual(before, c.toolCacheOps) {
			t.Fatal("invalid replacement changed receiver")
		}
	}
}
