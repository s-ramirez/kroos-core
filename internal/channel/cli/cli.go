// Package cli implements a channel.Channel backed by stdin/stdout. It
// prints a prompt, reads user input line by line, and writes replies
// straight to the configured writer.
package cli

import (
	"bufio"
	"context"
	"io"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/channel"
)

const (
	// Name is the channel name used for routing. CLI conversations use
	// this plus DefaultThread as their key.
	Name          = "cli"
	DefaultThread = "default"
	prompt        = "> "
)

// Channel is a channel.Channel implementation that reads user input from
// a bufio.Reader and writes replies to an io.Writer. It is safe for a
// process to instantiate at most one Channel; concurrent Listen calls
// would race on stdin.
type Channel struct {
	Reader *bufio.Reader
	Writer io.Writer
}

// NewStdio returns a Channel wired to the process stdin and stdout.
func NewStdio(stdin io.Reader, stdout io.Writer) *Channel {
	return &Channel{
		Reader: bufio.NewReader(stdin),
		Writer: stdout,
	}
}

func (c *Channel) Name() string { return Name }

// Terminal: ending the CLI (Ctrl-D) ends the process.
func (c *Channel) Terminal() bool { return true }

// Listen writes a prompt, reads one line, pushes it onto the bus, and
// loops. It exits on EOF or ctx cancellation.
func (c *Channel) Listen(ctx context.Context, out chan<- channel.Inbound) error {
	for {
		if _, err := io.WriteString(c.Writer, prompt); err != nil {
			return err
		}

		line, err := c.readLine(ctx)
		if err != nil {
			return err
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		msg := channel.Inbound{
			Channel:    Name,
			Thread:     DefaultThread,
			Sender:     "user",
			Content:    trimmed,
			Kind:       channel.KindUser,
			ReceivedAt: time.Now(),
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- msg:
		}
	}
}

// Send writes the outbound content to stdout verbatim. Callers are
// expected to include any trailing newline they want.
func (c *Channel) Send(ctx context.Context, msg channel.Outbound) error {
	_ = ctx
	_, err := io.WriteString(c.Writer, msg.Content)
	return err
}

// readLine reads one line from the reader. It honors ctx cancellation
// by running the blocking read in a goroutine and selecting against
// ctx.Done(); the goroutine leaks until stdin delivers a line or hits
// EOF, which is acceptable for a process-level stdin reader.
func (c *Channel) readLine(ctx context.Context) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := c.Reader.ReadString('\n')
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				ch <- result{line: strings.TrimRight(line, "\r\n")}
				return
			}
			ch <- result{err: err}
			return
		}
		ch <- result{line: strings.TrimRight(line, "\r\n")}
	}()

	select {
	case r := <-ch:
		return r.line, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
