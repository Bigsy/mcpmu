package mcp

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestSSEScanner_BasicEvent(t *testing.T) {
	input := "data: hello world\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(event.Data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", event.Data)
	}
	if event.ID != "" {
		t.Errorf("expected empty ID, got %q", event.ID)
	}
	if event.Event != "" {
		t.Errorf("expected empty event type, got %q", event.Event)
	}
}

func TestSSEScanner_EventWithID(t *testing.T) {
	input := "id: 42\nevent: message\ndata: {\"jsonrpc\":\"2.0\"}\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.ID != "42" {
		t.Errorf("expected ID '42', got %q", event.ID)
	}
	if event.Event != "message" {
		t.Errorf("expected event 'message', got %q", event.Event)
	}
	if string(event.Data) != `{"jsonrpc":"2.0"}` {
		t.Errorf("unexpected data: %q", event.Data)
	}
}

func TestSSEScanner_MultilineData(t *testing.T) {
	// Multi-line data should be joined with newlines
	input := "data: line1\ndata: line2\ndata: line3\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "line1\nline2\nline3"
	if string(event.Data) != expected {
		t.Errorf("expected %q, got %q", expected, event.Data)
	}
}

func TestSSEScanner_CommentLines(t *testing.T) {
	// Comment lines (starting with :) should be ignored
	input := ": this is a comment\ndata: actual data\n: another comment\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(event.Data) != "actual data" {
		t.Errorf("expected 'actual data', got %q", event.Data)
	}
}

func TestSSEScanner_MultipleEvents(t *testing.T) {
	input := "id: 1\ndata: first\n\nid: 2\ndata: second\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	// First event
	event1, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error reading first event: %v", err)
	}
	if event1.ID != "1" || string(event1.Data) != "first" {
		t.Errorf("first event: got ID=%q Data=%q", event1.ID, event1.Data)
	}

	// Second event
	event2, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error reading second event: %v", err)
	}
	if event2.ID != "2" || string(event2.Data) != "second" {
		t.Errorf("second event: got ID=%q Data=%q", event2.ID, event2.Data)
	}

	// EOF
	_, err = scanner.Next()
	if err != io.EOF {
		t.Errorf("expected EOF, got %v", err)
	}
}

func TestSSEScanner_LeadingSpaceInValue(t *testing.T) {
	// Leading single space in value should be stripped
	input := "data:  two spaces\ndata: one space\ndata:no space\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// First "data:  two spaces" -> " two spaces" (one space stripped)
	// Second "data: one space" -> "one space"
	// Third "data:no space" -> "no space"
	expected := " two spaces\none space\nno space"
	if string(event.Data) != expected {
		t.Errorf("expected %q, got %q", expected, event.Data)
	}
}

func TestSSEScanner_EmptyData(t *testing.T) {
	// Event with no data lines
	input := "id: 123\nevent: ping\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.ID != "123" {
		t.Errorf("expected ID '123', got %q", event.ID)
	}
	if event.Event != "ping" {
		t.Errorf("expected event 'ping', got %q", event.Event)
	}
	if len(event.Data) != 0 {
		t.Errorf("expected empty data, got %q", event.Data)
	}
}

func TestSSEScanner_CRLFLineEndings(t *testing.T) {
	input := "data: test\r\n\r\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(event.Data) != "test" {
		t.Errorf("expected 'test', got %q", event.Data)
	}
}

func TestSSEScanner_MaxSizeExceeded(t *testing.T) {
	// Create event larger than max size
	largeData := bytes.Repeat([]byte("x"), 100)
	input := "data: " + string(largeData) + "\n\n"
	scanner := newSSEScanner(strings.NewReader(input), 50) // Small max size

	_, err := scanner.Next()
	if err == nil {
		t.Error("expected error for oversized event")
	}
	if !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSSEScanner_FieldWithoutValue(t *testing.T) {
	// Field without colon or value
	input := "data\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "data" without colon should be treated as field with no value
	// Per SSE spec, this appends empty string to data buffer
	if len(event.Data) != 0 {
		t.Errorf("expected empty data, got %q", event.Data)
	}
}

func TestSSEScanner_KeepAliveComment(t *testing.T) {
	// Keep-alive is typically just a comment
	input := ":\n\ndata: actual\n\n"
	scanner := newSSEScanner(strings.NewReader(input), MaxSSEEventSize)

	// First should be the actual data event (comment-only "events" are skipped)
	event, err := scanner.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(event.Data) != "actual" {
		t.Errorf("expected 'actual', got %q", event.Data)
	}
}

// Tests for WWW-Authenticate header parsing (RFC 9728 OAuth support)

func TestParseWWWAuthenticate_Full(t *testing.T) {
	header := `Bearer realm="example", resource_metadata="https://auth.example.com/.well-known/oauth-protected-resource", scope="read write"`
	challenge := parseWWWAuthenticate(header)

	if challenge == nil {
		t.Fatal("expected non-nil challenge")
	}
	if challenge.ResourceMetadata != "https://auth.example.com/.well-known/oauth-protected-resource" {
		t.Errorf("ResourceMetadata = %q, want URL", challenge.ResourceMetadata)
	}
	if challenge.Realm != "example" {
		t.Errorf("Realm = %q, want 'example'", challenge.Realm)
	}
	if challenge.Scope != "read write" {
		t.Errorf("Scope = %q, want 'read write'", challenge.Scope)
	}
}

func TestParseWWWAuthenticate_ResourceMetadataOnly(t *testing.T) {
	header := `Bearer resource_metadata="https://mcp.figma.com/.well-known/oauth-protected-resource"`
	challenge := parseWWWAuthenticate(header)

	if challenge == nil {
		t.Fatal("expected non-nil challenge")
	}
	if challenge.ResourceMetadata != "https://mcp.figma.com/.well-known/oauth-protected-resource" {
		t.Errorf("ResourceMetadata = %q", challenge.ResourceMetadata)
	}
}

func TestParseWWWAuthenticate_CaseInsensitive(t *testing.T) {
	header := `bearer Resource_Metadata="https://example.com/meta"`
	challenge := parseWWWAuthenticate(header)

	if challenge == nil {
		t.Fatal("expected non-nil challenge")
	}
	if challenge.ResourceMetadata != "https://example.com/meta" {
		t.Errorf("ResourceMetadata = %q", challenge.ResourceMetadata)
	}
}

func TestParseWWWAuthenticate_EmptyHeader(t *testing.T) {
	challenge := parseWWWAuthenticate("")
	if challenge != nil {
		t.Errorf("expected nil for empty header, got %+v", challenge)
	}
}

func TestParseWWWAuthenticate_NonBearer(t *testing.T) {
	challenge := parseWWWAuthenticate("Basic realm=\"example\"")
	if challenge != nil {
		t.Errorf("expected nil for non-Bearer scheme, got %+v", challenge)
	}
}

func TestParseWWWAuthenticate_BearerOnly(t *testing.T) {
	challenge := parseWWWAuthenticate("Bearer")
	if challenge == nil {
		t.Fatal("expected non-nil challenge for bare Bearer")
	}
	// Should have empty fields
	if challenge.ResourceMetadata != "" || challenge.Realm != "" || challenge.Scope != "" {
		t.Errorf("expected empty fields, got %+v", challenge)
	}
}

func TestUnauthorizedError(t *testing.T) {
	unauthErr := &UnauthorizedError{
		Challenge: &AuthChallenge{
			ResourceMetadata: "https://example.com/meta",
			Realm:            "test",
		},
	}

	if unauthErr.Error() != "unauthorized - authentication required" {
		t.Errorf("unexpected error message: %s", unauthErr.Error())
	}

	// Test that error implements error interface
	var e error = unauthErr
	if e.Error() == "" {
		t.Error("UnauthorizedError should return non-empty error message")
	}
}
