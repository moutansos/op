package serveproc

import (
	"slices"
	"testing"
)

func TestNextGenerationEnvIncrementsAndReplaces(t *testing.T) {
	first := NextGenerationEnv([]string{"HOME=/home/me"})
	if !slices.Equal(first, []string{"HOME=/home/me", GenerationEnv + "=1"}) {
		t.Fatalf("first = %q", first)
	}
	second := NextGenerationEnv(first)
	if !slices.Equal(second, []string{"HOME=/home/me", GenerationEnv + "=2"}) {
		t.Fatalf("second = %q", second)
	}
}

func TestUnitFromCgroup(t *testing.T) {
	tests := []struct {
		name, cgroup, unit string
		user               bool
	}{
		{"user unit", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/op.service\n", "op.service", true},
		{"system unit", "0::/system.slice/op.service\n", "op.service", false},
		{"terminal scope", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-kitty-123.scope\n", "", false},
		{"login session", "0::/user.slice/user-1000.slice/session-2.scope\n", "", false},
		{"cgroup v1", "12:pids:/user.slice\n1:name=systemd:/user.slice/user-1000.slice/user@1000.service/op.service\n", "op.service", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unit, user := unitFromCgroup(test.cgroup)
			if unit != test.unit || user != test.user {
				t.Fatalf("unit=%q user=%v", unit, user)
			}
		})
	}
}
