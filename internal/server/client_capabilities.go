package server

import "encoding/json"

// Client features and modes a relayed server-to-client request may need; see
// Session.clientSupports.
const (
	featureElicitation = "elicitation"
	featureSampling    = "sampling"
	featureRoots       = "roots"

	modeForm        = "form"        // elicitation
	modeURL         = "url"         // elicitation
	modeTools       = "tools"       // sampling with tool use
	modeListChanged = "listChanged" // roots
)

// clientCapabilities is what a downstream client declared at initialize that
// matters for relaying: a request is only ever relayed to a client that said
// it can handle it, in the mode it needs.
type clientCapabilities struct {
	elicitationForm  bool
	elicitationURL   bool
	sampling         bool
	samplingTools    bool
	roots            bool
	rootsListChanged bool
}

// parseClientCapabilities reads the capabilities object from initialize.
// Malformed or absent members declare nothing.
func parseClientCapabilities(raw json.RawMessage) clientCapabilities {
	var caps struct {
		Elicitation *struct {
			Form *json.RawMessage `json:"form"`
			URL  *json.RawMessage `json:"url"`
		} `json:"elicitation"`
		Sampling *struct {
			Tools *json.RawMessage `json:"tools"`
		} `json:"sampling"`
		Roots *struct {
			ListChanged bool `json:"listChanged"`
		} `json:"roots"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &caps) != nil {
		return clientCapabilities{}
	}
	var out clientCapabilities
	if e := caps.Elicitation; e != nil {
		// An empty elicitation object is the pre-2025-11-25 declaration and
		// means form mode only.
		out.elicitationForm = e.Form != nil || e.URL == nil
		out.elicitationURL = e.URL != nil
	}
	if s := caps.Sampling; s != nil {
		out.sampling = true
		out.samplingTools = s.Tools != nil
	}
	if r := caps.Roots; r != nil {
		out.roots = true
		out.rootsListChanged = r.ListChanged
	}
	return out
}

// supports reports whether the client declared feature, and mode within it
// when mode is non-empty.
func (c clientCapabilities) supports(feature, mode string) bool {
	switch feature {
	case featureElicitation:
		switch mode {
		case "", modeForm:
			return c.elicitationForm
		case modeURL:
			return c.elicitationURL
		}
	case featureSampling:
		switch mode {
		case "":
			return c.sampling
		case modeTools:
			return c.samplingTools
		}
	case featureRoots:
		switch mode {
		case "":
			return c.roots
		case modeListChanged:
			return c.rootsListChanged
		}
	}
	return false
}

// clientSupports reports whether this session's client declared feature (and
// mode, when non-empty) at initialize. Before initialize nothing is supported.
func (s *Session) clientSupports(feature, mode string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clientCaps.supports(feature, mode)
}
