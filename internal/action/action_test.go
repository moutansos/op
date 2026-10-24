package action

import (
	"testing"
)

func TestReservedSyntaxUsesCaseSensitiveBuiltInDispatchRules(t *testing.T) {
	tests := []struct {
		id       string
		reserved bool
	}{
		{id: "nvim", reserved: true},
		{id: "code", reserved: true},
		{id: "shell", reserved: true},
		{id: "worktree", reserved: true},
		{id: "worktree:feature", reserved: true},
		{id: "worktree:", reserved: true},
		{id: "Nvim"},
		{id: "Open shell logs"},
		{id: "my-worktree:feature"},
		{id: "worktree helper"},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			_, reserved := ReservedSyntax(test.id)
			if reserved != test.reserved {
				t.Fatalf("ReservedSyntax(%q) reserved = %v, want %v", test.id, reserved, test.reserved)
			}
		})
	}
}

func TestValidateCustomNameAcceptsNamesWithoutBuiltInSyntax(t *testing.T) {
	for _, name := range []string{"0", "00", "1", "03", "v2", "3-way", "+3"} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCustomName(name); err != nil {
				t.Fatalf("ValidateCustomName(%q) error = %v", name, err)
			}
		})
	}
}
