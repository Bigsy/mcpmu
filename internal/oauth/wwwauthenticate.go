package oauth

import (
	"net/http"
	"strings"
)

// BearerChallenge holds parsed WWW-Authenticate Bearer parameters.
// This enables RFC 9728 OAuth Protected Resource Metadata discovery.
type BearerChallenge struct {
	// ResourceMetadata is the URL from resource_metadata="..." parameter.
	// This is the key field for RFC 9728 Protected Resource Metadata.
	ResourceMetadata string

	// Realm from realm="..." parameter.
	Realm string

	// Scope from scope="..." parameter.
	Scope string
}

// ParseBearerChallenge extracts Bearer challenge info from HTTP response headers.
// It scans all WWW-Authenticate header values for a Bearer challenge with parameters.
// Returns nil if no Bearer challenge is found.
func ParseBearerChallenge(headers http.Header) *BearerChallenge {
	return ParseBearerChallengeValues(headers.Values("WWW-Authenticate"))
}

// ParseBearerChallengeValues extracts Bearer challenge info from WWW-Authenticate values.
// This is the testable core that processes multiple header values.
func ParseBearerChallengeValues(values []string) *BearerChallenge {
	for _, value := range values {
		challenges := parseWWWAuthenticateValue(value)
		for _, ch := range challenges {
			if strings.EqualFold(ch.scheme, "bearer") {
				return &BearerChallenge{
					ResourceMetadata: ch.params["resource_metadata"],
					Realm:            ch.params["realm"],
					Scope:            ch.params["scope"],
				}
			}
		}
	}
	return nil
}

// authChallenge represents a single parsed authentication challenge.
type authChallenge struct {
	scheme string
	params map[string]string
}

// parseWWWAuthenticateValue parses a single WWW-Authenticate header value.
// Per RFC 7235, a header value can contain multiple challenges separated by commas.
// This is tricky because parameters are also comma-separated.
//
// Examples:
//   - "Bearer realm=\"foo\""
//   - "Basic realm=\"bar\", Bearer resource_metadata=\"https://...\""
//   - "Bearer realm=\"foo\", scope=\"openid\", resource_metadata=\"https://...\""
func parseWWWAuthenticateValue(value string) []authChallenge {
	var challenges []authChallenge
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	// Tokenize the header value
	tokens := tokenizeAuthHeader(value)
	if len(tokens) == 0 {
		return nil
	}

	// Parse challenges from tokens
	// A challenge starts with a scheme token (no '='), followed by auth-params
	var current *authChallenge
	i := 0
	for i < len(tokens) {
		tok := tokens[i]

		// Check if this token is a scheme (not followed by '=')
		// A scheme is a token that:
		// 1. Is not an auth-param (no = sign after it in the same param)
		// 2. Looks like an auth-scheme (letter start, alphanumeric)
		if isSchemeToken(tokens, i) {
			// Start a new challenge
			if current != nil {
				challenges = append(challenges, *current)
			}
			current = &authChallenge{
				scheme: tok,
				params: make(map[string]string),
			}
			i++
			continue
		}

		// This should be a key=value param for the current challenge
		if current != nil && i+2 < len(tokens) && tokens[i+1] == "=" {
			key := strings.ToLower(tok)
			val := tokens[i+2]
			current.params[key] = val
			i += 3
			continue
		}

		// Skip unexpected tokens (commas between params, etc)
		i++
	}

	if current != nil {
		challenges = append(challenges, *current)
	}

	return challenges
}

// isSchemeToken determines if a token at position i is an auth-scheme.
// An auth-scheme is followed by either:
// - Nothing (bare scheme)
// - A token that is NOT '=' (another scheme or param key for this scheme)
// - A space and then params (token68 or auth-params)
func isSchemeToken(tokens []string, i int) bool {
	if i >= len(tokens) {
		return false
	}
	tok := tokens[i]

	// Must look like a scheme (starts with letter, alphanumeric/-/+/.)
	if len(tok) == 0 || !isLetter(tok[0]) {
		return false
	}
	for j := 1; j < len(tok); j++ {
		c := tok[j]
		if !isAlphaNum(c) && c != '-' && c != '+' && c != '.' {
			return false
		}
	}

	// If next token is '=', this is a key, not a scheme
	if i+1 < len(tokens) && tokens[i+1] == "=" {
		return false
	}

	return true
}

// tokenizeAuthHeader tokenizes a WWW-Authenticate header value.
// It handles quoted strings (which can contain commas) and separators.
func tokenizeAuthHeader(value string) []string {
	var tokens []string
	i := 0
	n := len(value)

	for i < n {
		// Skip whitespace and commas (param separators)
		for i < n && (value[i] == ' ' || value[i] == '\t' || value[i] == ',') {
			i++
		}
		if i >= n {
			break
		}

		// Check for '=' (keep as separate token)
		if value[i] == '=' {
			tokens = append(tokens, "=")
			i++
			continue
		}

		// Check for quoted string
		if value[i] == '"' {
			str, end := parseQuotedString(value, i)
			tokens = append(tokens, str)
			i = end
			continue
		}

		// Parse token (alphanumeric and allowed chars until separator)
		start := i
		for i < n && isTokenChar(value[i]) {
			i++
		}
		if i > start {
			tokens = append(tokens, value[start:i])
		} else {
			// Skip unexpected character (e.g., '/' in token68 values like Negotiate)
			// to ensure we always make progress and avoid infinite loops
			i++
		}
	}

	return tokens
}

// parseQuotedString parses a quoted string starting at position i.
// Returns the unquoted value and the position after the closing quote.
func parseQuotedString(s string, i int) (string, int) {
	if i >= len(s) || s[i] != '"' {
		return "", i
	}
	i++ // skip opening quote

	var result strings.Builder
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			// Quoted-pair: backslash escapes next char
			result.WriteByte(s[i+1])
			i += 2
			continue
		}
		if s[i] == '"' {
			// End of quoted string
			return result.String(), i + 1
		}
		result.WriteByte(s[i])
		i++
	}
	// Unterminated quote - return what we have
	return result.String(), i
}

// isTokenChar returns true if c is valid in an HTTP token (RFC 7230).
func isTokenChar(c byte) bool {
	// token = 1*tchar
	// tchar = "!" / "#" / "$" / "%" / "&" / "'" / "*" / "+" / "-" / "." /
	//         "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA
	if isAlphaNum(c) {
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func isLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isAlphaNum(c byte) bool {
	return isLetter(c) || (c >= '0' && c <= '9')
}
