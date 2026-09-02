package server

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/Bigsy/mcpmu/internal/config"
)

// Wrapper tool names. None contains a dot, so they can never collide with a
// qualified `{server}.{tool}` name — an upstream tool literally named
// "invoke_tool" stays reachable as "{server}.invoke_tool".
const (
	wrapperListTools     = "list_tools"
	wrapperGetToolSchema = "get_tool_schema"
	wrapperInvokeTool    = "invoke_tool"
)

// getToolSchemaArgs accepts exactly one of Tool or Tools.
type getToolSchemaArgs struct {
	Tool  string   `json:"tool"`
	Tools []string `json:"tools"`
}

type invokeToolArgs struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

// wrapperTools builds the compressed client-visible surface. The listing is
// embedded in invoke_tool's description at every level — including high/max —
// so the model sees the available tools on the first tools/list without an
// extra call. The level shapes only the listing string itself, which the
// caller has already rendered.
func wrapperTools(listing string) []AggregatedTool {
	invokeDescription := "Call a tool by its qualified name. Fetch the tool's schema with " +
		wrapperGetToolSchema + " before first use. Available tools:\n" + listing
	return []AggregatedTool{
		{
			Name: wrapperListTools,
			Description: "List the tools available through this endpoint in compact " +
				"`server.tool_name(args)` form. Use " + wrapperGetToolSchema +
				" for a tool's full input schema and " + wrapperInvokeTool + " to call it.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		},
		{
			Name: wrapperGetToolSchema,
			Description: "Get the full definition (description, input/output schema, annotations) " +
				"for tools listed by " + wrapperListTools + ". Pass exactly one of \"tool\" or \"tools\".",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {` +
				`"tool": {"type": "string", "description": "Qualified tool name (server.tool_name)"}, ` +
				`"tools": {"type": "array", "items": {"type": "string"}, "description": "Multiple qualified tool names; results are returned per entry"}}}`),
		},
		{
			Name:        wrapperInvokeTool,
			Description: invokeDescription,
			InputSchema: json.RawMessage(`{"type": "object", "properties": {` +
				`"tool": {"type": "string", "description": "Qualified tool name (server.tool_name)"}, ` +
				`"input": {"type": "object", "description": "Arguments for the tool, matching its input schema"}}, ` +
				`"required": ["tool"]}`),
		},
	}
}

// formatListing renders the compact one-line-per-tool listing. Tools arrive in
// exposed form (qualified name, "[server]" description prefix) and in the same
// order handleToolsList produces, so the output is stable across calls.
func formatListing(level config.CompressionLevel, tools []AggregatedTool) string {
	if len(tools) == 0 {
		// A deny-by-default namespace with no allows, or one with no servers,
		// has nothing to list — but a bare "Available tools:" header with
		// nothing under it invites the model to invent a name. Say so.
		return "(none)"
	}
	var b strings.Builder
	for i, t := range tools {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("<tool>")
		b.WriteString(t.Name)
		if level != config.CompressionMax {
			b.WriteByte('(')
			b.WriteString(strings.Join(schemaArgNames(t.InputSchema), ", "))
			b.WriteByte(')')
		}
		var desc string
		switch level {
		case config.CompressionLow:
			desc = listingDescription(t)
		case config.CompressionMedium:
			desc = firstSentence(listingDescription(t))
		}
		if desc != "" {
			b.WriteString(": ")
			b.WriteString(desc)
		}
		b.WriteString("</tool>")
	}
	return b.String()
}

// listingDescription flattens a tool description to a single line and drops
// the "[server]" prefix exposeTool added — redundant next to the qualified
// name that opens the listing line.
func listingDescription(t AggregatedTool) string {
	desc := t.Description
	if serverName, _, _ := ParseToolName(t.Name); serverName != "" {
		desc = strings.TrimPrefix(desc, "["+serverName+"]")
	}
	desc = strings.Join(strings.Fields(desc), " ")
	return desc
}

// maxSentenceLen caps a medium-level listing line; a description whose first
// "sentence" is a wall of text should not defeat the compression.
const maxSentenceLen = 200

// firstSentence returns text up to the first "." followed by whitespace or
// end-of-string, capped at maxSentenceLen runes. Dots inside tokens
// ("config.json") do not end the sentence; a mid-text abbreviation like
// "e.g. " does, and that imprecision is accepted.
func firstSentence(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '.' {
			continue
		}
		if i+1 == len(s) {
			break
		}
		switch s[i+1] {
		case ' ', '\t', '\n', '\r':
			s = s[:i+1]
		default:
			continue
		}
		break
	}
	if runes := []rune(s); len(runes) > maxSentenceLen {
		return string(runes[:maxSentenceLen]) + "…"
	}
	return s
}

// schemaArgNames extracts the argument names of an object input schema:
// required args first, then optional args suffixed with "?", each group in
// the order the author wrote the properties. The walk uses json.Decoder
// tokens because unmarshalling into a map loses key order and would shuffle
// args between calls. A missing, invalid, or non-object schema yields nil,
// rendering as "name()".
func schemaArgNames(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(schema))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var props, required []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil
		}
		switch key {
		case "properties":
			if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
				return nil
			}
			for dec.More() {
				nameTok, err := dec.Token()
				if err != nil {
					return nil
				}
				name, ok := nameTok.(string)
				if !ok {
					return nil
				}
				props = append(props, name)
				var skip json.RawMessage
				if err := dec.Decode(&skip); err != nil {
					return nil
				}
			}
			if _, err := dec.Token(); err != nil { // closing }
				return nil
			}
		case "required":
			if err := dec.Decode(&required); err != nil {
				return nil
			}
		default:
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil
			}
		}
	}
	requiredSet := make(map[string]bool, len(required))
	for _, r := range required {
		requiredSet[r] = true
	}
	args := make([]string, 0, len(props))
	for _, p := range props {
		if requiredSet[p] {
			args = append(args, p)
		}
	}
	for _, p := range props {
		if !requiredSet[p] {
			args = append(args, p+"?")
		}
	}
	return args
}
