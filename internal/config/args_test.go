package config

import (
	"slices"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{"", nil, false},
		{"   ", nil, false},
		{"-y pkg", []string{"-y", "pkg"}, false},
		{"  a   b\tc\n d ", []string{"a", "b", "c", "d"}, false},
		{`--name "hello world"`, []string{"--name", "hello world"}, false},
		{`--name 'hello world'`, []string{"--name", "hello world"}, false},
		{`"it's"`, []string{"it's"}, false},
		{`a\ b`, []string{"a b"}, false},
		{`"say \"hi\""`, []string{`say "hi"`}, false},
		{`""`, []string{""}, false},
		{`pre"fix"ed`, []string{"prefixed"}, false},
		{`"unterminated`, nil, true},
		{`trailing\`, nil, true},
	}
	for _, tc := range cases {
		got, err := SplitArgs(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("SplitArgs(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && !slices.Equal(got, tc.want) {
			t.Errorf("SplitArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestJoinArgsRoundTrip(t *testing.T) {
	cases := [][]string{
		nil,
		{"-y", "pkg"},
		{"hello world"},
		{`say "hi"`},
		{`back\slash`},
		{"it's"},
		{""},
		{"tab\there"},
	}
	for _, args := range cases {
		s := JoinArgs(args)
		got, err := SplitArgs(s)
		if err != nil {
			t.Errorf("SplitArgs(JoinArgs(%q)=%q): %v", args, s, err)
			continue
		}
		if len(args) == 0 && len(got) == 0 {
			continue
		}
		if !slices.Equal(got, args) {
			t.Errorf("round trip %q → %q → %q", args, s, got)
		}
	}
}
