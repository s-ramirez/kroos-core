package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/s-ramirez/kroos-core/internal/llm"
)

// runLogin handles `kroos login [chatgpt]` and `kroos logout [chatgpt]`.
func runLogin(cmd string, args []string, w io.Writer) error {
	provider := llm.ProviderChatGPT
	if len(args) > 0 {
		provider = args[0]
	}
	if provider != llm.ProviderChatGPT {
		return fmt.Errorf("%q needs no login (only %s does; omlx uses omlx.api_key)", provider, llm.ProviderChatGPT)
	}
	path := llm.DefaultAuthPath()
	if cmd == "logout" {
		if err := llm.ChatGPTLogout(path); err != nil {
			return err
		}
		fmt.Fprintln(w, "Logged out of ChatGPT.")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := llm.ChatGPTLogin(ctx, path, w); err != nil {
		return err
	}
	fmt.Fprintf(w, "Logged in to ChatGPT. Tokens saved to %s\n", path)
	return nil
}
