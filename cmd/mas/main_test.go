package main

import (
	"strings"
	"testing"
)

// Flags must be honoured when they follow the search term, and bad input must fail
// before any request is made (these cases never touch the network).
func TestRunRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing term", []string{"search", "--json"}, "missing search term"},
		{"limit too high", []string{"search", "leche", "--limit", "101", "--json"}, "--limit must be between 1 and 100"},
		{"limit not a number", []string{"search", "leche", "--limit", "muchos"}, "invalid value"},
		{"page zero", []string{"search", "leche", "--page", "0"}, "--page must be 1 or more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			err := run(tt.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing on error", stdout.String())
			}
		})
	}
}
