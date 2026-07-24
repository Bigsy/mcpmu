package config

import (
	"slices"
	"strings"
	"testing"
)

// TestReservedServerNameRejected guards the one server name that serve mode
// cannot route.
//
// Tools are exposed as "<server>.<tool>" and the aggregator treats the "mcpmu."
// prefix as its own manager namespace. A server called mcpmu was accepted by
// ValidateName, so its tools were listed, skipped the permission filter (the
// isManager branch in Session.handleToolsList appends and continues before
// IsToolAllowed runs), and failed on call with "tool not found" — listed,
// unfilterable, permanently uncallable.
func TestReservedServerNameRejected(t *testing.T) {
	if err := ValidateServerName(ReservedServerName); err == nil {
		t.Fatalf("ValidateServerName(%q) = nil, want an error", ReservedServerName)
	} else if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error %q should explain that the name is reserved", err)
	}

	// Namespaces are unaffected: they never appear in a qualified tool name.
	if err := ValidateName(ReservedServerName); err != nil {
		t.Errorf("ValidateName(%q) = %v, want nil (namespaces may use it)", ReservedServerName, err)
	}

	// Names that merely start with the reserved one are fine — the aggregator
	// matches the exact "mcpmu." prefix, so "mcpmu2.tool" is unambiguous.
	for _, ok := range []string{"mcpmu2", "mcpmu-extra", "my-mcpmu", "MCPMU"} {
		if err := ValidateServerName(ok); err != nil {
			t.Errorf("ValidateServerName(%q) = %v, want nil", ok, err)
		}
	}
}

// TestReservedServerNameRejectedOnAddAndRename covers the two config entry
// points every caller funnels through: the CLI, TUI and web add/rename paths all
// reach AddServer or RenameServer.
func TestReservedServerNameRejectedOnAddAndRename(t *testing.T) {
	cfg := NewConfig()

	if err := cfg.AddServer(ReservedServerName, ServerConfig{Command: "echo"}); err == nil {
		t.Errorf("AddServer(%q) = nil, want an error", ReservedServerName)
	}
	if _, exists := cfg.Servers[ReservedServerName]; exists {
		t.Errorf("AddServer(%q) stored the server despite failing", ReservedServerName)
	}

	if err := cfg.AddServer("real", ServerConfig{Command: "echo"}); err != nil {
		t.Fatalf("AddServer(\"real\"): %v", err)
	}
	if err := cfg.RenameServer("real", ReservedServerName); err == nil {
		t.Errorf("RenameServer to %q = nil, want an error", ReservedServerName)
	}
	// A rejected rename must leave the original in place.
	if _, exists := cfg.Servers["real"]; !exists {
		t.Error("failed rename removed the original server")
	}
}

// TestReservedNameConflictsDoesNotBlockLoad records a deliberate choice: an
// existing config containing the reserved name still loads. Refusing the whole
// file would take every other server down over one bad name, which is worse
// than the broken routing it would prevent; serve warns on stderr instead.
func TestReservedNameConflictsDoesNotBlockLoad(t *testing.T) {
	cfg := NewConfig()
	// Bypass AddServer the way a hand-edited config file does.
	cfg.Servers[ReservedServerName] = ServerConfig{Command: "echo"}
	cfg.Servers["fine"] = ServerConfig{Command: "echo"}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil: an existing config must still load", err)
	}

	got := cfg.ReservedNameConflicts()
	if !slices.Equal(got, []string{ReservedServerName}) {
		t.Errorf("ReservedNameConflicts() = %v, want [%q]", got, ReservedServerName)
	}
}
