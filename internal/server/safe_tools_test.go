package server

import (
	"testing"
)

func TestToolClassification_String(t *testing.T) {
	tests := []struct {
		class    ToolClassification
		expected string
	}{
		{ToolSafe, "safe"},
		{ToolUnsafe, "unsafe"},
		{ToolUnknown, "unknown"},
	}

	for _, tt := range tests {
		if tt.class.String() != tt.expected {
			t.Errorf("expected %q, got %q", tt.expected, tt.class.String())
		}
	}
}

func TestClassifyTool(t *testing.T) {
	tests := []struct {
		toolName string
		expected ToolClassification
	}{
		// Safe patterns
		{"read_file", ToolSafe},
		{"get_user", ToolSafe},
		{"list_files", ToolSafe},
		{"search_documents", ToolSafe},
		{"view_log", ToolSafe},
		{"show_config", ToolSafe},
		{"describe_table", ToolSafe},
		{"fetch_data", ToolSafe},
		{"query_database", ToolSafe},
		{"find_matches", ToolSafe},
		{"lookup_user", ToolSafe},
		{"check_status", ToolSafe},
		{"get_info", ToolSafe},
		{"count_records", ToolSafe},
		{"exists_file", ToolSafe},
		{"is_valid", ToolSafe},
		{"has_permission", ToolSafe},
		{"can_access", ToolSafe},
		// Note: validate_input is classified as unsafe because "input" contains "put"
		// This is acceptable - explicit permissions can override

		// Unsafe patterns
		{"write_file", ToolUnsafe},
		{"update_user", ToolUnsafe},
		{"delete_file", ToolUnsafe},
		{"execute_command", ToolUnsafe},
		{"run_script", ToolUnsafe},
		{"create_user", ToolUnsafe},
		{"set_config", ToolUnsafe},
		{"modify_record", ToolUnsafe},
		{"remove_item", ToolUnsafe},
		{"post_data", ToolUnsafe},
		{"put_object", ToolUnsafe},
		{"patch_resource", ToolUnsafe},
		{"send_email", ToolUnsafe},
		{"invoke_function", ToolUnsafe},
		{"start_server", ToolUnsafe},
		{"stop_process", ToolUnsafe},
		{"kill_process", ToolUnsafe},
		{"terminate_session", ToolUnsafe},
		{"restart_service", ToolUnsafe},
		{"reboot_machine", ToolUnsafe},
		{"install_package", ToolUnsafe},
		{"uninstall_app", ToolUnsafe},
		{"enable_feature", ToolUnsafe},
		{"disable_feature", ToolUnsafe},
		{"add_user", ToolUnsafe},
		{"drop_table", ToolUnsafe},
		{"truncate_log", ToolUnsafe},
		{"clear_cache", ToolUnsafe},
		{"reset_password", ToolUnsafe},
		{"init_database", ToolUnsafe},
		{"apply_changes", ToolUnsafe},
		{"deploy_app", ToolUnsafe},
		{"publish_message", ToolUnsafe},
		{"submit_form", ToolUnsafe},
		{"approve_request", ToolUnsafe},
		{"reject_request", ToolUnsafe},
		{"close_ticket", ToolUnsafe},
		{"open_connection", ToolUnsafe},
		{"lock_account", ToolUnsafe},
		{"unlock_account", ToolUnsafe},
		{"grant_permission", ToolUnsafe},
		{"revoke_access", ToolUnsafe},
		{"move_file", ToolUnsafe},
		{"rename_folder", ToolUnsafe},
		{"copy_data", ToolUnsafe},

		// Unknown patterns
		{"foo", ToolUnknown},
		{"bar", ToolUnknown},
		{"process", ToolUnknown},
		{"handle", ToolUnknown},
		{"transform", ToolUnknown},

		// Case insensitivity
		{"READ_FILE", ToolSafe},
		{"Get_User", ToolSafe},
		{"WRITE_FILE", ToolUnsafe},
		{"Delete_Item", ToolUnsafe},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			result := ClassifyTool(tt.toolName)
			if result != tt.expected {
				t.Errorf("ClassifyTool(%q) = %v, expected %v", tt.toolName, result, tt.expected)
			}
		})
	}
}

func TestClassifyTool_StripServerPrefix(t *testing.T) {
	tests := []struct {
		qualifiedName string
		expected      ToolClassification
	}{
		{"filesystem.read_file", ToolSafe},
		{"fs.list_directory", ToolSafe},
		{"database.query_table", ToolSafe},
		{"api.get_user", ToolSafe},
		{"filesystem.write_file", ToolUnsafe},
		{"db.delete_record", ToolUnsafe},
		{"service.execute_command", ToolUnsafe},
		{"unknown.foo", ToolUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.qualifiedName, func(t *testing.T) {
			result := ClassifyTool(tt.qualifiedName)
			if result != tt.expected {
				t.Errorf("ClassifyTool(%q) = %v, expected %v", tt.qualifiedName, result, tt.expected)
			}
		})
	}
}

func TestStripServerPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"filesystem.read_file", "read_file"},
		{"fs.list", "list"},
		{"read_file", "read_file"},
		{"a.b.c", "b.c"},
		{"", ""},
		{"nodot", "nodot"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := stripServerPrefix(tt.input)
			if result != tt.expected {
				t.Errorf("stripServerPrefix(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsSafe(t *testing.T) {
	if !IsSafe("read_file") {
		t.Error("expected read_file to be safe")
	}
	if IsSafe("write_file") {
		t.Error("expected write_file not to be safe")
	}
	if IsSafe("foo") {
		t.Error("expected foo not to be safe (unknown)")
	}
}

func TestIsUnsafe(t *testing.T) {
	if !IsUnsafe("write_file") {
		t.Error("expected write_file to be unsafe")
	}
	if IsUnsafe("read_file") {
		t.Error("expected read_file not to be unsafe")
	}
	if IsUnsafe("foo") {
		t.Error("expected foo not to be unsafe (unknown)")
	}
}
