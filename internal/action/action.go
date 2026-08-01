// Package action defines project action IDs shared by configuration and runtime dispatch.
package action

import (
	"fmt"
	"strings"
)

const (
	NvimID     = "nvim"
	CodeID     = "code"
	ShellID    = "shell"
	WorktreeID = "worktree"
)

type Kind uint8

const (
	Custom Kind = iota
	Nvim
	Code
	Shell
	Worktree
)

// Classify applies the case-sensitive built-in action dispatch rules.
func Classify(id string) Kind {
	switch id {
	case NvimID:
		return Nvim
	case CodeID:
		return Code
	case ShellID:
		return Shell
	case WorktreeID:
		return Worktree
	default:
		if strings.HasPrefix(id, WorktreeID+":") {
			return Worktree
		}
		return Custom
	}
}

// ReservedSyntax reports the built-in ID or syntax with which a custom name collides.
func ReservedSyntax(id string) (string, bool) {
	switch Classify(id) {
	case Nvim:
		return NvimID, true
	case Code:
		return CodeID, true
	case Shell:
		return ShellID, true
	case Worktree:
		if id == WorktreeID {
			return WorktreeID, true
		}
		return WorktreeID + ":<branch>", true
	default:
		return "", false
	}
}

// ValidateCustomName rejects names interpreted as built-in actions.
func ValidateCustomName(name string) error {
	if reserved, collision := ReservedSyntax(name); collision {
		return fmt.Errorf("must not collide with reserved built-in action %q", reserved)
	}
	return nil
}

// WorktreeBranch extracts a branch only from the built-in worktree syntax.
func WorktreeBranch(id string) (string, bool) {
	return strings.CutPrefix(id, WorktreeID+":")
}
