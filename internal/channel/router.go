package channel

import "context"

// Router is a TurnHandler that picks a handler per conversation key.
// Keys present in Routes go to their bound handler (e.g. a profile
// runner); everything else falls through to Default.
type Router struct {
	Default TurnHandler
	Routes  map[Key]TurnHandler
}

func (r *Router) HandleTurn(ctx context.Context, conv *Conversation, userContent string, reply ReplyFunc) error {
	if h, ok := r.Routes[conv.Key]; ok {
		return h.HandleTurn(ctx, conv, userContent, reply)
	}
	return r.Default.HandleTurn(ctx, conv, userContent, reply)
}
