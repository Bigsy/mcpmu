package oauth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestCallbackServer_RandomPort(t *testing.T) {
	server, err := NewCallbackServer(nil)
	if err != nil {
		t.Fatalf("NewCallbackServer failed: %v", err)
	}
	defer func() { _ = server.Stop() }()

	if server.Port() == 0 {
		t.Error("Port should not be 0")
	}

	uri := server.RedirectURI()
	if uri == "" {
		t.Error("RedirectURI is empty")
	}
}

func TestCallbackServer_SpecificPort(t *testing.T) {
	port := 18080
	server, err := NewCallbackServer(&port)
	if err != nil {
		t.Fatalf("NewCallbackServer failed: %v", err)
	}
	defer func() { _ = server.Stop() }()

	if server.Port() != port {
		t.Errorf("Port: got %d, want %d", server.Port(), port)
	}

	expectedURI := "http://127.0.0.1:18080/callback"
	if server.RedirectURI() != expectedURI {
		t.Errorf("RedirectURI: got %q, want %q", server.RedirectURI(), expectedURI)
	}
}

func TestCallbackServer_ZeroPortRandom(t *testing.T) {
	port := 0
	server, err := NewCallbackServer(&port)
	if err != nil {
		t.Fatalf("NewCallbackServer failed: %v", err)
	}
	defer func() { _ = server.Stop() }()

	if server.Port() == 0 {
		t.Error("Port should not be 0")
	}
}

func TestCallbackServer_Callback(t *testing.T) {
	server, err := NewCallbackServer(nil)
	if err != nil {
		t.Fatalf("NewCallbackServer failed: %v", err)
	}
	defer func() { _ = server.Stop() }()

	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Simulate callback in goroutine
	go func() {
		time.Sleep(100 * time.Millisecond)
		callbackURL := server.RedirectURI() + "?code=test-code&state=test-state"
		resp, err := http.Get(callbackURL)
		if err != nil {
			t.Errorf("Callback request failed: %v", err)
			return
		}
		_ = resp.Body.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := server.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	if result.Code != "test-code" {
		t.Errorf("Code: got %q, want %q", result.Code, "test-code")
	}
	if result.State != "test-state" {
		t.Errorf("State: got %q, want %q", result.State, "test-state")
	}
}

func TestCallbackServer_ErrorCallback(t *testing.T) {
	server, err := NewCallbackServer(nil)
	if err != nil {
		t.Fatalf("NewCallbackServer failed: %v", err)
	}
	defer func() { _ = server.Stop() }()

	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Simulate error callback
	go func() {
		time.Sleep(100 * time.Millisecond)
		callbackURL := server.RedirectURI() + "?error=access_denied&error_description=User+denied+access"
		resp, err := http.Get(callbackURL)
		if err != nil {
			t.Errorf("Callback request failed: %v", err)
			return
		}
		_ = resp.Body.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := server.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}

	if result.Error != "access_denied" {
		t.Errorf("Error: got %q, want %q", result.Error, "access_denied")
	}
	if result.ErrorDescription != "User denied access" {
		t.Errorf("ErrorDescription: got %q, want %q", result.ErrorDescription, "User denied access")
	}
}

func TestCallbackServer_Timeout(t *testing.T) {
	server, err := NewCallbackServer(nil)
	if err != nil {
		t.Fatalf("NewCallbackServer failed: %v", err)
	}
	defer func() { _ = server.Stop() }()

	if err := server.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait with short timeout - should timeout without callback
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = server.Wait(ctx)
	if err != context.DeadlineExceeded {
		t.Errorf("Expected DeadlineExceeded, got %v", err)
	}
}
