// Package events provides the event system for mcpmu.
package events

import (
	"encoding/json"
	"time"
)

// RuntimeState represents the current state of a server process.
type RuntimeState int

const (
	StateIdle RuntimeState = iota
	StateStarting
	StateRunning
	StateStopping
	StateStopped
	StateError
	StateCrashed
	StateNeedsAuth // Server requires OAuth login before connecting
)

func (s RuntimeState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateError:
		return "error"
	case StateCrashed:
		return "crashed"
	case StateNeedsAuth:
		return "needs-auth"
	default:
		return "unknown"
	}
}

// IsActive returns true if the server is in a running or transitioning state.
func (s RuntimeState) IsActive() bool {
	return s == StateStarting || s == StateRunning || s == StateStopping
}

// LastExit contains information about the last process exit.
type LastExit struct {
	Code      int       `json:"code"`
	Signal    string    `json:"signal,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ServerStatus represents the current runtime status of a server.
type ServerStatus struct {
	ID        string       `json:"id"`
	State     RuntimeState `json:"state"`
	PID       int          `json:"pid,omitempty"`
	LastExit  *LastExit    `json:"lastExit,omitempty"`
	ToolCount int          `json:"toolCount"`
	Error     string       `json:"error,omitempty"`
	StartedAt *time.Time   `json:"startedAt,omitempty"`
}

// McpTool represents a tool exposed by an MCP server.
type McpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// EventType identifies the kind of event.
type EventType int

const (
	EventStatusChanged EventType = iota
	EventLogReceived
	EventToolsUpdated
	EventError
)

func (e EventType) String() string {
	switch e {
	case EventStatusChanged:
		return "status_changed"
	case EventLogReceived:
		return "log_received"
	case EventToolsUpdated:
		return "tools_updated"
	case EventError:
		return "error"
	default:
		return "unknown"
	}
}

// Event is the base interface for all events.
type Event interface {
	Type() EventType
	ServerID() string
	Timestamp() time.Time
}

// baseEvent provides common fields for all events.
type baseEvent struct {
	serverID  string
	timestamp time.Time
}

func (e baseEvent) ServerID() string    { return e.serverID }
func (e baseEvent) Timestamp() time.Time { return e.timestamp }

// StatusChangedEvent is emitted when a server's state changes.
type StatusChangedEvent struct {
	baseEvent
	OldState RuntimeState
	NewState RuntimeState
	Status   ServerStatus
}

func (e StatusChangedEvent) Type() EventType { return EventStatusChanged }

// NewStatusChangedEvent creates a new status changed event.
func NewStatusChangedEvent(serverID string, oldState, newState RuntimeState, status ServerStatus) StatusChangedEvent {
	return StatusChangedEvent{
		baseEvent: baseEvent{serverID: serverID, timestamp: time.Now()},
		OldState:  oldState,
		NewState:  newState,
		Status:    status,
	}
}

// LogReceivedEvent is emitted when stderr output is received from a server.
type LogReceivedEvent struct {
	baseEvent
	Line string
}

func (e LogReceivedEvent) Type() EventType { return EventLogReceived }

// NewLogReceivedEvent creates a new log received event.
func NewLogReceivedEvent(serverID, line string) LogReceivedEvent {
	return LogReceivedEvent{
		baseEvent: baseEvent{serverID: serverID, timestamp: time.Now()},
		Line:      line,
	}
}

// ToolsUpdatedEvent is emitted when tools are discovered or change.
type ToolsUpdatedEvent struct {
	baseEvent
	Tools []McpTool
}

func (e ToolsUpdatedEvent) Type() EventType { return EventToolsUpdated }

// NewToolsUpdatedEvent creates a new tools updated event.
func NewToolsUpdatedEvent(serverID string, tools []McpTool) ToolsUpdatedEvent {
	return ToolsUpdatedEvent{
		baseEvent: baseEvent{serverID: serverID, timestamp: time.Now()},
		Tools:     tools,
	}
}

// ErrorEvent is emitted when an error occurs.
type ErrorEvent struct {
	baseEvent
	Err     error
	Message string
}

func (e ErrorEvent) Type() EventType { return EventError }

// NewErrorEvent creates a new error event.
func NewErrorEvent(serverID string, err error, message string) ErrorEvent {
	return ErrorEvent{
		baseEvent: baseEvent{serverID: serverID, timestamp: time.Now()},
		Err:       err,
		Message:   message,
	}
}
