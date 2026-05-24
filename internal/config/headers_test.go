package config

import (
	"strings"
	"testing"
)

func TestParseHeaderLines(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr string // substring match; "" means no error
	}{
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "only whitespace and comments",
			input: "\n  \n# a comment\n",
			want:  nil,
		},
		{
			name:  "single header",
			input: "X-Foo: bar",
			want:  map[string]string{"X-Foo": "bar"},
		},
		{
			name:  "no space after colon",
			input: "X-Foo:bar",
			want:  map[string]string{"X-Foo": "bar"},
		},
		{
			name:  "leading and trailing space in value",
			input: "X-Foo:   bar   ",
			want:  map[string]string{"X-Foo": "bar"},
		},
		{
			name:  "value containing colon",
			input: "Authorization: Bearer abc:def",
			want:  map[string]string{"Authorization": "Bearer abc:def"},
		},
		{
			name:  "multiple headers",
			input: "X-Foo: a\nX-Bar: b",
			want:  map[string]string{"X-Foo": "a", "X-Bar": "b"},
		},
		{
			name:  "interior whitespace preserved",
			input: "X-Foo: a  b  c",
			want:  map[string]string{"X-Foo": "a  b  c"},
		},
		{
			name:  "cf access pair",
			input: "CF-Access-Client-Id: id\nCF-Access-Client-Secret: secret",
			want: map[string]string{
				"CF-Access-Client-Id":     "id",
				"CF-Access-Client-Secret": "secret",
			},
		},
		{
			name:    "missing colon",
			input:   "X-Foo bar",
			wantErr: "missing ':'",
		},
		{
			name:    "empty name",
			input:   ": foo",
			wantErr: "empty header name",
		},
		{
			name:    "duplicate name",
			input:   "X-Foo: a\nX-Foo: b",
			wantErr: "duplicate",
		},
		{
			name:    "invalid name with space",
			input:   "X Foo: bar",
			wantErr: "invalid header name",
		},
		{
			name:    "invalid name with control char",
			input:   "X\x00Foo: bar",
			wantErr: "invalid header name",
		},
		{
			name:    "value with CR",
			input:   "X-Foo: bar\rX-Admin: 1",
			wantErr: "CR or LF",
		},
		{
			name:    "value with control char",
			input:   "X-Foo: bar\x01baz",
			wantErr: "control character",
		},
		{
			name:    "value with DEL",
			input:   "X-Foo: bar\x7fbaz",
			wantErr: "DEL",
		},
		{
			name:  "tab in value allowed",
			input: "X-Foo: a\tb",
			want:  map[string]string{"X-Foo": "a\tb"},
		},
		{
			name:  "comment lines ignored",
			input: "# comment\nX-Foo: bar\n# another",
			want:  map[string]string{"X-Foo": "bar"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHeaderLines(tt.input)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (result: %v)", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d headers (%v), want %d (%v)", len(got), got, len(tt.want), tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("got[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestFormatHeaderLines(t *testing.T) {
	t.Run("empty map", func(t *testing.T) {
		if got := FormatHeaderLines(nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("sorted by name", func(t *testing.T) {
		got := FormatHeaderLines(map[string]string{
			"X-Foo": "1",
			"A-Bar": "2",
			"M-Baz": "3",
		})
		want := "A-Bar: 2\nM-Baz: 3\nX-Foo: 1"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("roundtrip", func(t *testing.T) {
		input := map[string]string{
			"CF-Access-Client-Id":     "id",
			"CF-Access-Client-Secret": "secret",
			"X-Custom":                "value with spaces",
		}
		s := FormatHeaderLines(input)
		got, err := ParseHeaderLines(s)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		if len(got) != len(input) {
			t.Fatalf("got %d, want %d", len(got), len(input))
		}
		for k, v := range input {
			if got[k] != v {
				t.Errorf("got[%q] = %q, want %q", k, got[k], v)
			}
		}
	})
}

func TestValidateHeaderMap(t *testing.T) {
	tests := []struct {
		name    string
		m       map[string]string
		wantErr string
	}{
		{name: "valid", m: map[string]string{"X-Foo": "bar"}},
		{name: "empty input is fine", m: nil},
		{name: "invalid name", m: map[string]string{"X Foo": "bar"}, wantErr: "invalid header name"},
		{name: "empty name", m: map[string]string{"": "bar"}, wantErr: "empty header name"},
		{name: "CRLF in value", m: map[string]string{"X-Foo": "a\r\nb"}, wantErr: "CR or LF"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHeaderMap(tt.m)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
