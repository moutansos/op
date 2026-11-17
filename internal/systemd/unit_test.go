package systemd

import (
	"strings"
	"testing"
)

func TestRenderUnit(t *testing.T) {
	unit := RenderUnit(UnitSpec{Executable: "/home/me/.local/bin/op", ConfigPath: "/home/me/.config/op/config.json", Path: "/home/me/bin:/usr/bin"})
	for _, want := range []string{
		"\nEnvironment=PATH=/home/me/bin:/usr/bin\n",
		"\nExecStart=/home/me/.local/bin/op --config /home/me/.config/op/config.json serve\n",
		"\nRestart=on-failure\n",
		"\nWantedBy=default.target\n",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRenderUnitEscapesSpecialCharacters(t *testing.T) {
	unit := RenderUnit(UnitSpec{Executable: "/opt/my op/op", ConfigPath: `/cfg/100%$"x".json`, Path: "/a b:/c$d"})
	for _, want := range []string{
		`Environment="PATH=/a b:/c$d"`,
		`ExecStart="/opt/my op/op" --config "/cfg/100%%$$\"x\".json" serve`,
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestRenderUnitOmitsMissingConfigAndPath(t *testing.T) {
	unit := RenderUnit(UnitSpec{Executable: "/usr/bin/op"})
	if strings.Contains(unit, "Environment=") || !strings.Contains(unit, "ExecStart=/usr/bin/op serve\n") {
		t.Fatalf("unit:\n%s", unit)
	}
}

func TestUserUnitDir(t *testing.T) {
	env := map[string]string{"HOME": "/home/me"}
	lookup := func(name string) string { return env[name] }
	if dir, _ := UserUnitDir(lookup); dir != "/home/me/.config/systemd/user" {
		t.Fatalf("dir = %q", dir)
	}
	env["XDG_CONFIG_HOME"] = "/xdg"
	if dir, _ := UserUnitDir(lookup); dir != "/xdg/systemd/user" {
		t.Fatalf("dir = %q", dir)
	}
}
