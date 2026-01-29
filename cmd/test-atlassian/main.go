// +build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hedworth/mcp-studio-go/internal/mcp"
	"github.com/hedworth/mcp-studio-go/internal/oauth"
)

func main() {
	serverURL := "https://mcp.atlassian.com/v1/sse"

	// Get token from credential store
	store, err := oauth.NewCredentialStore(oauth.StoreModeAuto)
	if err != nil {
		log.Fatalf("Failed to create credential store: %v", err)
	}

	tokenManager := oauth.NewTokenManager(store)
	ctx := context.Background()

	token, err := tokenManager.GetAccessToken(ctx, serverURL)
	if err != nil {
		log.Fatalf("Failed to get access token: %v", err)
	}
	fmt.Printf("Got access token (length: %d)\n", len(token))

	// Create HTTP transport
	config := mcp.StreamableHTTPConfig{
		URL:         serverURL,
		BearerToken: token,
	}
	transport := mcp.NewStreamableHTTPTransport(config)

	// Connect SSE stream
	fmt.Println("Connecting to SSE stream...")
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := transport.Connect(connectCtx); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer transport.Close()

	fmt.Printf("Connected! Session ID: %s\n", transport.SessionID())

	// Create MCP client
	client := mcp.NewClient(transport)

	// Initialize
	fmt.Println("Initializing MCP...")
	initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
	defer initCancel()

	if err := client.Initialize(initCtx); err != nil {
		log.Fatalf("Failed to initialize: %v", err)
	}

	name, version := client.ServerInfo()
	fmt.Printf("Server: %s v%s\n", name, version)

	// List tools
	fmt.Println("Listing tools...")
	toolsCtx, toolsCancel := context.WithTimeout(ctx, 30*time.Second)
	defer toolsCancel()

	tools, err := client.ListTools(toolsCtx)
	if err != nil {
		log.Fatalf("Failed to list tools: %v", err)
	}

	fmt.Printf("Found %d tools:\n", len(tools))
	for _, tool := range tools {
		fmt.Printf("  - %s: %s\n", tool.Name, truncate(tool.Description, 60))
	}

	// Test actual tool invocation
	fmt.Println("\nTesting tool invocation: atlassianUserInfo...")
	callCtx, callCancel := context.WithTimeout(ctx, 30*time.Second)
	defer callCancel()

	result, err := client.CallTool(callCtx, "atlassianUserInfo", json.RawMessage(`{}`))
	if err != nil {
		log.Fatalf("Failed to call tool: %v", err)
	}

	fmt.Printf("Tool result (isError=%v):\n", result.IsError)
	for i, content := range result.Content {
		fmt.Printf("  [%d]: %s\n", i, truncate(string(content), 200))
	}

	fmt.Println("\nSuccess! Atlassian MCP server is working end-to-end.")
	os.Exit(0)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
