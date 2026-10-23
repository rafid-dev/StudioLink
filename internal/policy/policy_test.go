package policy

import "testing"

func TestAllows(t *testing.T) {
	tests := []struct {
		name   string
		policy *Tools
		tool   string
		want   bool
	}{
		{"nil allows all", nil, "execute_luau", true},
		{"empty allows all", &Tools{}, "execute_luau", true},
		{"denied", &Tools{Deny: []string{"execute_luau"}}, "execute_luau", false},
		{"deny is case-insensitive", &Tools{Deny: []string{"execute_luau"}}, "Execute_Luau", false},
		{"other tool", &Tools{Deny: []string{"execute_luau"}}, "script_read", true},
		{"deny wildcard", &Tools{Deny: []string{"generate_*"}}, "generate_mesh", false},
		{"allow list", &Tools{Allow: []string{"script_*", "list_roblox_studios"}}, "script_grep", true},
		{"not in allow list", &Tools{Allow: []string{"script_*"}}, "multi_edit", false},
		{"deny beats allow", &Tools{Allow: []string{"script_*"}, Deny: []string{"script_read"}}, "script_read", false},
	}
	for _, tt := range tests {
		if got := tt.policy.Allows(tt.tool); got != tt.want {
			t.Errorf("%s: Allows(%q) = %v, want %v", tt.name, tt.tool, got, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := (&Tools{Deny: []string{"[bad"}}).Validate(); err == nil {
		t.Error("bad pattern accepted")
	}
	if err := (&Tools{Allow: []string{" "}}).Validate(); err == nil {
		t.Error("empty name accepted")
	}
	if err := (&Tools{Allow: []string{"script_*"}, Deny: []string{"execute_luau"}}).Validate(); err != nil {
		t.Error(err)
	}
}
