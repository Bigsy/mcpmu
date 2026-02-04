package oauth

import (
	"net/http"
	"testing"
)

func TestParseBearerChallengeValues(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		want     *BearerChallenge
		wantNil  bool
	}{
		{
			name:    "empty values",
			values:  nil,
			wantNil: true,
		},
		{
			name:    "empty string",
			values:  []string{""},
			wantNil: true,
		},
		{
			name:   "simple bearer",
			values: []string{`Bearer realm="example"`},
			want: &BearerChallenge{
				Realm: "example",
			},
		},
		{
			name:   "bearer with resource_metadata",
			values: []string{`Bearer resource_metadata="https://api.example.com/.well-known/oauth-protected-resource"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://api.example.com/.well-known/oauth-protected-resource",
			},
		},
		{
			name:   "bearer with all params",
			values: []string{`Bearer realm="example", scope="openid profile", resource_metadata="https://auth.example.com/resource-metadata"`},
			want: &BearerChallenge{
				Realm:            "example",
				Scope:            "openid profile",
				ResourceMetadata: "https://auth.example.com/resource-metadata",
			},
		},
		{
			name:   "bearer case insensitive scheme",
			values: []string{`BEARER resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "bearer case insensitive params",
			values: []string{`Bearer Resource_Metadata="https://example.com", REALM="test"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://example.com",
				Realm:            "test",
			},
		},
		{
			name:   "basic then bearer - bearer in same value",
			values: []string{`Basic realm="foo", Bearer resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "bearer then basic - returns bearer",
			values: []string{`Bearer resource_metadata="https://example.com", Basic realm="foo"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "multiple header values - bearer in second",
			values: []string{`Basic realm="foo"`, `Bearer resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "multiple header values - bearer in first",
			values: []string{`Bearer resource_metadata="https://example.com"`, `Basic realm="foo"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:    "only basic auth",
			values:  []string{`Basic realm="foo"`},
			wantNil: true,
		},
		{
			name:   "quoted value with comma",
			values: []string{`Bearer realm="hello, world", resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				Realm:            "hello, world",
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "quoted value with spaces",
			values: []string{`Bearer scope="openid profile email", resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				Scope:            "openid profile email",
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "quoted value with escaped quote",
			values: []string{`Bearer realm="test \"quoted\"", resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				Realm:            `test "quoted"`,
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "unquoted value",
			values: []string{`Bearer realm=example, resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				Realm:            "example",
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "figma-style header",
			values: []string{`Bearer resource_metadata="https://mcp.figma.com/.well-known/oauth-protected-resource"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://mcp.figma.com/.well-known/oauth-protected-resource",
			},
		},
		{
			name: "multiple challenges across multiple values",
			values: []string{
				`Digest realm="digest-realm", nonce="abc123"`,
				`Basic realm="basic-realm"`,
				`Bearer realm="bearer-realm", resource_metadata="https://auth.example.com"`,
			},
			want: &BearerChallenge{
				Realm:            "bearer-realm",
				ResourceMetadata: "https://auth.example.com",
			},
		},
		{
			name:   "bearer with trailing comma",
			values: []string{`Bearer resource_metadata="https://example.com",`},
			want: &BearerChallenge{
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "bearer with extra whitespace",
			values: []string{`Bearer   realm="test"  ,  resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				Realm:            "test",
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:   "bare bearer scheme",
			values: []string{`Bearer`},
			want:   &BearerChallenge{},
		},
		{
			name:   "bearer with space after scheme",
			values: []string{`Bearer `},
			want:   &BearerChallenge{},
		},
		{
			name:   "negotiate with token68 before bearer - no infinite loop",
			values: []string{`Negotiate YIIK/gYGKwYBBQUC, Bearer resource_metadata="https://example.com"`},
			want: &BearerChallenge{
				ResourceMetadata: "https://example.com",
			},
		},
		{
			name:    "only negotiate with token68 - no infinite loop",
			values:  []string{`Negotiate YIIKhgYJKoZIhvcSAQICAQBug==`},
			wantNil: true,
		},
		{
			name:   "bearer after scheme with special chars",
			values: []string{`Custom (foo/bar), Bearer realm="test"`},
			want: &BearerChallenge{
				Realm: "test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseBearerChallengeValues(tt.values)
			if tt.wantNil {
				if got != nil {
					t.Errorf("ParseBearerChallengeValues() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Errorf("ParseBearerChallengeValues() = nil, want %+v", tt.want)
				return
			}
			if got.ResourceMetadata != tt.want.ResourceMetadata {
				t.Errorf("ResourceMetadata = %q, want %q", got.ResourceMetadata, tt.want.ResourceMetadata)
			}
			if got.Realm != tt.want.Realm {
				t.Errorf("Realm = %q, want %q", got.Realm, tt.want.Realm)
			}
			if got.Scope != tt.want.Scope {
				t.Errorf("Scope = %q, want %q", got.Scope, tt.want.Scope)
			}
		})
	}
}

func TestParseBearerChallenge(t *testing.T) {
	// Test the http.Header wrapper
	headers := http.Header{}
	headers.Add("WWW-Authenticate", `Basic realm="foo"`)
	headers.Add("WWW-Authenticate", `Bearer resource_metadata="https://example.com"`)

	got := ParseBearerChallenge(headers)
	if got == nil {
		t.Fatal("ParseBearerChallenge() = nil, want non-nil")
	}
	if got.ResourceMetadata != "https://example.com" {
		t.Errorf("ResourceMetadata = %q, want %q", got.ResourceMetadata, "https://example.com")
	}
}

func TestParseWWWAuthenticateValue(t *testing.T) {
	// Internal function tests for edge cases
	tests := []struct {
		name       string
		value      string
		wantCount  int
		wantScheme string
	}{
		{
			name:       "empty",
			value:      "",
			wantCount:  0,
			wantScheme: "",
		},
		{
			name:       "single scheme no params",
			value:      "Bearer",
			wantCount:  1,
			wantScheme: "Bearer",
		},
		{
			name:       "two schemes",
			value:      "Basic realm=\"foo\", Bearer realm=\"bar\"",
			wantCount:  2,
			wantScheme: "Basic", // first one
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWWWAuthenticateValue(tt.value)
			if len(got) != tt.wantCount {
				t.Errorf("parseWWWAuthenticateValue() returned %d challenges, want %d", len(got), tt.wantCount)
				return
			}
			if tt.wantCount > 0 && got[0].scheme != tt.wantScheme {
				t.Errorf("first scheme = %q, want %q", got[0].scheme, tt.wantScheme)
			}
		})
	}
}

func TestTokenizeAuthHeader(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{
			name:  "simple",
			value: `Bearer realm="test"`,
			want:  []string{"Bearer", "realm", "=", "test"},
		},
		{
			name:  "quoted with comma",
			value: `Bearer realm="a, b"`,
			want:  []string{"Bearer", "realm", "=", "a, b"},
		},
		{
			name:  "two schemes",
			value: `Basic realm="one", Bearer realm="two"`,
			want:  []string{"Basic", "realm", "=", "one", "Bearer", "realm", "=", "two"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenizeAuthHeader(tt.value)
			if len(got) != len(tt.want) {
				t.Errorf("tokenizeAuthHeader() = %v (len %d), want %v (len %d)", got, len(got), tt.want, len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("token[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
