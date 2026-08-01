package tmux

import "testing"

func TestCreatedWindowID(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   string
		valid  bool
	}{
		{name: "newline", output: "@12\n", want: "@12", valid: true},
		{name: "CRLF", output: "@0\r\n", want: "@0", valid: true},
		{name: "without newline", output: "@7", want: "@7", valid: true},
		{name: "empty", output: "", valid: false},
		{name: "only newline", output: "\n", valid: false},
		{name: "multiple lines", output: "@1\n@2\n", valid: false},
		{name: "multiple trailing newlines", output: "@1\n\n", valid: false},
		{name: "missing prefix", output: "1\n", valid: false},
		{name: "missing number", output: "@\n", valid: false},
		{name: "zero with leading zero", output: "@00\n", valid: false},
		{name: "positive with leading zero", output: "@01\n", valid: false},
		{name: "non-decimal", output: "@1x\n", valid: false},
		{name: "surrounding whitespace", output: " @1 \n", valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := createdWindowID(test.output)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("createdWindowID(%q) = %q, %v; want %q", test.output, got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("createdWindowID(%q) = %q, nil; want error", test.output, got)
			}
		})
	}
}
