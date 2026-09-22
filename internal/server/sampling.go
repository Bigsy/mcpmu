package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

// samplingRequesterMetaKey names the upstream server in a relayed
// sampling/createMessage's _meta. The client only knows it is talking to
// mcpmu; this identifies who wants to spend its model tokens without touching
// the messages or the system prompt the model will see.
const samplingRequesterMetaKey = "mcpmu/server"

// outcomeSampled is the interaction outcome for a sampling request the
// client completed.
const outcomeSampled = "completed"

// samplingUnavailable is the answer when a sampling request cannot be
// relayed. Sampling has no "cancel" action to fall back on, so it is an error.
func samplingUnavailable(reason string) *mcp.RPCError {
	return &mcp.RPCError{Code: ErrCodeInternalError, Message: "Sampling request not relayed: " + reason}
}

// relaySampling answers sampling/createMessage from an upstream. Sampling
// spends the client's model tokens, so it is relayed only on certain or
// strong evidence — a private instance, or an HTTP upstream's POST stream —
// never on the single-caller heuristic; and a request offering the model
// tools only to a client that declared sampling.tools, from a server opted
// in to it.
func (c *Core) relaySampling(ctx context.Context, req process.UpstreamRequest) (json.RawMessage, *mcp.RPCError) {
	server := req.Instance.Server
	record := func(namespace, outcome string) {
		c.recordInteraction(namespace, server, req.Method, outcome)
	}

	var params map[string]json.RawMessage
	if err := json.Unmarshal(req.Params, &params); err != nil || params == nil {
		record("", fallbackMalformedParams)
		return nil, samplingUnavailable("malformed params")
	}
	_, withTools := params["tools"]
	if _, ok := params["toolChoice"]; ok {
		withTools = true
	}

	target, rule, ok := c.routeServerRequest(req, false)
	if !ok {
		log.Printf("sampling from %s (id %s): no certain route (%d calls in flight, origin hint: %t)",
			req.Instance, req.ID, len(req.InFlight), req.Origin != nil)
		record("", fallbackUnroutable)
		return nil, samplingUnavailable("it could not be tied to a single client session")
	}
	session := target.session
	namespace := session.activeNamespace()
	if DebugLogging {
		log.Printf("sampling from %s (id %s) routed to %s by rule %s", req.Instance, req.ID, session.id, rule)
	}
	if !session.clientSupports(featureSampling, "") {
		record(namespace, fallbackUnsupported)
		return nil, samplingUnavailable("the client does not support sampling")
	}
	cfg := c.currentConfig()
	srv, _ := cfg.GetServer(server)
	if withTools && (!srv.SamplingToolsEnabled() || !session.clientSupports(featureSampling, modeTools)) {
		record(namespace, fallbackUnsupported)
		return nil, samplingUnavailable("tool use in sampling is not enabled for this server or client")
	}

	var meta map[string]json.RawMessage
	if raw, ok := params["_meta"]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	if meta == nil {
		meta = make(map[string]json.RawMessage, 1)
	}
	meta[samplingRequesterMetaKey], _ = json.Marshal(server)
	params["_meta"], _ = json.Marshal(meta)

	result, rpcErr, err := target.interact(ctx, req.Method, params, cfg.InteractionTimeout(srv))
	switch {
	case err != nil:
		reason := interactionFailureOutcome(ctx, err)
		if DebugLogging || reason != fallbackUpstreamGaveUp {
			log.Printf("sampling from %s (id %s) not answered: %v", req.Instance, req.ID, err)
		}
		record(namespace, reason)
		return nil, samplingUnavailable(samplingFailureReason(err))
	case rpcErr != nil:
		record(namespace, outcomeClientError)
		return nil, &mcp.RPCError{Code: rpcErr.Code, Message: rpcErr.Message, Data: rpcErr.Data}
	}
	record(namespace, outcomeSampled)
	return result, nil
}

func samplingFailureReason(err error) string {
	switch {
	case errors.Is(err, errInteractionTimeout):
		return "the client did not answer in time"
	case errors.Is(err, errOwningCallEnded):
		return "the call that asked for it has ended"
	case errors.Is(err, errInteractionBudgetSpent):
		return "the call's interaction budget is spent"
	case errors.Is(err, errSessionClosed):
		return "the client session closed"
	}
	return "it could not be delivered to the client"
}
