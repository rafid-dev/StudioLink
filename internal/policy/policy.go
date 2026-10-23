// Package policy decides which Studio MCP tools a client may use.
package policy

import (
	"fmt"
	"path"
	"strings"
)

// Tools is an allow/deny list of tool names. Entries may use * and ? as
// wildcards ("generate_*"). A tool is usable when Allow is empty or matches
// it, and Deny does not match it.
type Tools struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// Allows reports whether the tool may be listed and called. A nil policy
// allows everything.
func (t *Tools) Allows(name string) bool {
	if t == nil {
		return true
	}
	if len(t.Allow) > 0 && !matchAny(t.Allow, name) {
		return false
	}
	return !matchAny(t.Deny, name)
}

// Validate checks that every entry is a usable pattern.
func (t *Tools) Validate() error {
	if t == nil {
		return nil
	}
	for _, p := range append(append([]string{}, t.Allow...), t.Deny...) {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("empty tool name")
		}
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("tool pattern %q: %w", p, err)
		}
	}
	return nil
}

// String summarises the policy for logs, for example "deny execute_luau".
func (t *Tools) String() string {
	if t == nil || (len(t.Allow) == 0 && len(t.Deny) == 0) {
		return "all tools"
	}
	var parts []string
	if len(t.Allow) > 0 {
		parts = append(parts, "allow "+strings.Join(t.Allow, ","))
	}
	if len(t.Deny) > 0 {
		parts = append(parts, "deny "+strings.Join(t.Deny, ","))
	}
	return strings.Join(parts, "; ")
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		// Tool names are case-sensitive in MCP, but a policy that misses
		// "Execute_Luau" because of case would fail open.
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(name)); ok {
			return true
		}
	}
	return false
}
