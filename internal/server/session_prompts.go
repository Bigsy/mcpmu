package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/Bigsy/mcpmu/internal/mcp"
)

// handlePromptsList handles the prompts/list request.
func (s *Session) handlePromptsList(ctx context.Context) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	type qualifiedPrompt struct {
		Name        string               `json:"name"`
		Description string               `json:"description,omitempty"`
		Arguments   []mcp.PromptArgument `json:"arguments,omitempty"`
	}

	results := make([][]mcp.Prompt, len(activeServerNames))
	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup

	for index, name := range activeServerNames {
		if !s.aggregatorForServer(name).shouldQueryCapability(name, catalogPrompts) {
			continue
		}
		wg.Add(1)
		resultIndex, serverName := index, name
		goSafe("prompts/list "+name, func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sc, rpcErr := s.getOrStartServer(ctx, serverName)
			if rpcErr != nil {
				log.Printf("Failed to get client for %s (prompts/list): %v", serverName, rpcErr)
				return
			}

			callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
			defer cancel()

			prompts, err := sc.client.ListPrompts(callCtx)
			if err != nil {
				log.Printf("Failed to list prompts from %s: %v", serverName, err)
				return
			}

			results[resultIndex] = prompts
		})
	}

	wg.Wait()

	allPrompts := make([]qualifiedPrompt, 0)
	for index, prompts := range results {
		serverName := activeServerNames[index]
		for _, p := range prompts {
			desc := p.Description
			if desc != "" {
				desc = fmt.Sprintf("[%s] %s", serverName, desc)
			} else {
				desc = fmt.Sprintf("[%s]", serverName)
			}
			allPrompts = append(allPrompts, qualifiedPrompt{
				Name:        serverName + "." + p.Name,
				Description: desc,
				Arguments:   p.Arguments,
			})
		}
	}
	return struct {
		Prompts []qualifiedPrompt `json:"prompts"`
	}{Prompts: allPrompts}, nil
}

// handlePromptsGet handles the prompts/get request.
func (s *Session) handlePromptsGet(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	var req struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// Split on first '.' to extract server name and original prompt name
	serverName, originalName, ok := strings.Cut(req.Name, ".")
	if !ok || serverName == "" || originalName == "" {
		return nil, ErrInvalidParams("invalid prompt name: " + req.Name)
	}

	if !slices.Contains(activeServerNames, serverName) {
		return nil, ErrServerNotFound(serverName)
	}

	sc, rpcErr := s.getOrStartServer(ctx, serverName)
	if rpcErr != nil {
		return nil, rpcErr
	}

	callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	messages, err := sc.client.GetPrompt(callCtx, originalName, req.Arguments)
	if err != nil {
		return nil, ErrInternalError(fmt.Sprintf("prompts/get from %s: %v", serverName, err))
	}

	return struct {
		Messages json.RawMessage `json:"messages"`
	}{Messages: messages}, nil
}
