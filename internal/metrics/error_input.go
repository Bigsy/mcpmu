package metrics

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
)

// redactErrorInput removes common secret fields recursively before inputs reach
// the recorder's buffers or disk. Free-text values are intentionally preserved;
// this is not a general-purpose personal-data scrubber.
func redactErrorInput(input json.RawMessage) string {
	if !json.Valid(input) {
		return "[input omitted: invalid JSON]"
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber() // Preserve large integer IDs and exact numeric literals.
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "[input omitted: invalid JSON]"
	}
	var redact func(any)
	redact = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if secretInputKey(key) {
					value[key] = "[REDACTED]"
				} else {
					redact(child)
				}
			}
		case []any:
			for _, child := range value {
				redact(child)
			}
		}
	}
	redact(value)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false) // The web template escapes the resulting text.
	if err := encoder.Encode(value); err != nil {
		return "[input omitted: could not encode JSON]"
	}
	return strings.TrimSuffix(output.String(), "\n")
}

func secretInputKey(key string) bool {
	key = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
	switch key {
	case "authorization", "proxyauthorization", "cookie", "cookies", "setcookie", "password", "passwd", "pwd", "secret", "secrets", "token", "apikey", "xapikey", "accesskey", "accesskeyid", "secretaccesskey", "credentials", "credential", "privatekey", "clientsecret", "bearertoken", "accesstoken", "refreshtoken", "idtoken", "sessiontoken":
		return true
	}
	for _, suffix := range []string{"password", "passwd", "secret", "apikey", "privatekey", "accesstoken", "refreshtoken", "bearertoken", "sessiontoken"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}
