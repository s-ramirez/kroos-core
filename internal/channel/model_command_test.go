package channel

import "testing"

func TestParseSlash(t *testing.T) {
	for in, want := range map[string]struct {
		args string
		ok   bool
	}{
		"/model":                {"", true},
		"/model 3":              {"3", true},
		"/model  omlx:gemma ":   {"omlx:gemma", true},
		"/models":               {"", false},
		"hello /model":          {"", false},
		"/skill:model whatever": {"", false},
	} {
		args, ok := parseSlash(in, "model")
		if args != want.args || ok != want.ok {
			t.Errorf("%q: got (%q,%v) want (%q,%v)", in, args, ok, want.args, want.ok)
		}
	}
}
