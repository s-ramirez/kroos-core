package channel

import (
	"context"
	"testing"
)

type namedHandler struct {
	name string
	got  *string
}

func (h namedHandler) HandleTurn(_ context.Context, _ *Conversation, _ string, _ ReplyFunc) error {
	*h.got = h.name
	return nil
}

func TestRouterDispatchesByKey(t *testing.T) {
	var got string
	bound := Key{Channel: "discord", Thread: "123"}
	r := &Router{
		Default: namedHandler{"main", &got},
		Routes:  map[Key]TurnHandler{bound: namedHandler{"ops", &got}},
	}

	cases := []struct {
		key  Key
		want string
	}{
		{bound, "ops"},
		{Key{Channel: "discord", Thread: "999"}, "main"},
		{Key{Channel: "cli", Thread: "123"}, "main"},
	}
	for _, c := range cases {
		got = ""
		if err := r.HandleTurn(context.Background(), &Conversation{Key: c.key}, "hi", nil); err != nil {
			t.Fatalf("HandleTurn: %v", err)
		}
		if got != c.want {
			t.Errorf("key %v: routed to %q, want %q", c.key, got, c.want)
		}
	}
}
