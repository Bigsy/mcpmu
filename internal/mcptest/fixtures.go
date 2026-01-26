package mcptest

import "time"

// Common test configurations for fake MCP servers.

// DefaultConfig returns a minimal working fake server configuration.
func DefaultConfig() FakeServerConfig {
	return FakeServerConfig{
		Tools: []Tool{
			{Name: "read_file", Description: "Read a file from disk"},
			{Name: "write_file", Description: "Write content to a file"},
		},
	}
}

// EmptyToolsConfig returns a config with no tools.
func EmptyToolsConfig() FakeServerConfig {
	return FakeServerConfig{
		Tools: []Tool{},
	}
}

// LargeToolListConfig returns a config with many tools (for performance testing).
func LargeToolListConfig(count int) FakeServerConfig {
	tools := make([]Tool, count)
	for i := 0; i < count; i++ {
		tools[i] = Tool{
			Name:        "tool_" + string(rune('a'+i%26)) + "_" + string(rune('0'+i/26)),
			Description: "A test tool for performance testing",
		}
	}
	return FakeServerConfig{Tools: tools}
}

// SlowInitConfig returns a config that delays the initialize response.
func SlowInitConfig(delay time.Duration) FakeServerConfig {
	return FakeServerConfig{
		Tools: []Tool{{Name: "test_tool"}},
		Delays: map[string]time.Duration{
			"initialize": delay,
		},
	}
}

// SlowToolsListConfig returns a config that delays the tools/list response.
func SlowToolsListConfig(delay time.Duration) FakeServerConfig {
	return FakeServerConfig{
		Tools: []Tool{{Name: "test_tool"}},
		Delays: map[string]time.Duration{
			"tools/list": delay,
		},
	}
}

// CrashOnInitConfig returns a config that crashes on initialize.
func CrashOnInitConfig(exitCode int) FakeServerConfig {
	return FakeServerConfig{
		CrashOnMethod: "initialize",
		CrashExitCode: exitCode,
	}
}

// CrashOnNthRequestConfig returns a config that crashes on the Nth request.
func CrashOnNthRequestConfig(n, exitCode int) FakeServerConfig {
	return FakeServerConfig{
		Tools:             []Tool{{Name: "test_tool"}},
		CrashOnNthRequest: n,
		CrashExitCode:     exitCode,
	}
}

// ErrorOnInitConfig returns a config that returns an error on initialize.
func ErrorOnInitConfig(code int, message string) FakeServerConfig {
	return FakeServerConfig{
		Errors: map[string]JSONRPCError{
			"initialize": {Code: code, Message: message},
		},
	}
}

// FailOnAttemptConfig returns a config that fails on a specific attempt of a method.
// Useful for testing retry logic.
func FailOnAttemptConfig(method string, attempt int) FakeServerConfig {
	return FakeServerConfig{
		Tools: []Tool{{Name: "test_tool"}},
		FailOnAttempt: map[string]int{
			method: attempt,
		},
	}
}

// NotificationBeforeResponseConfig returns a config that sends a notification before each response.
// Tests that clients properly skip notifications when waiting for responses.
func NotificationBeforeResponseConfig() FakeServerConfig {
	return FakeServerConfig{
		Tools:                          []Tool{{Name: "test_tool"}},
		SendNotificationBeforeResponse: true,
	}
}

// MismatchedIDConfig returns a config that sends a response with wrong ID before the correct one.
// Tests that clients properly match response IDs.
func MismatchedIDConfig() FakeServerConfig {
	return FakeServerConfig{
		Tools:                 []Tool{{Name: "test_tool"}},
		SendMismatchedIDFirst: true,
	}
}

// MalformedResponseConfig returns a config that sends invalid JSON.
func MalformedResponseConfig() FakeServerConfig {
	return FakeServerConfig{
		Malformed: true,
	}
}

// EchoToolsConfig returns a config that echoes tool calls back as text.
// Useful for testing tool call routing.
func EchoToolsConfig() FakeServerConfig {
	return FakeServerConfig{
		Tools: []Tool{
			{Name: "echo", Description: "Echo the input back"},
			{Name: "greet", Description: "Return a greeting"},
		},
		EchoToolCalls: true,
	}
}
