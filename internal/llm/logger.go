package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/s-ramirez/kroos-core/internal/types"
)

// LoggingClient wraps a ChatClient and logs every request and response
// as pretty-printed JSON files in a per-session directory.
type LoggingClient struct {
	inner  ChatClient
	logDir string
	seq    int
	mu     sync.Mutex
}

// NewLoggingClient wraps inner and writes logs to sessionDir.
// It creates sessionDir if it does not exist.
func NewLoggingClient(inner ChatClient, sessionDir string) (*LoggingClient, error) {
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	return &LoggingClient{inner: inner, logDir: sessionDir}, nil
}

func (l *LoggingClient) Chat(ctx context.Context, messages []types.Message, tools []types.ChatRequestTool) (*types.ChatResponse, error) {
	l.mu.Lock()
	seq := l.seq
	l.seq++
	l.mu.Unlock()

	ts := time.Now().Format("20060102-150405")
	prefix := fmt.Sprintf("%03d_%s", seq, ts)

	// Log request
	l.writeJSON(prefix+"_request.json", map[string]any{
		"messages": messages,
		"tools":    tools,
	})

	resp, err := l.inner.Chat(ctx, messages, tools)

	// Log response or error
	if err != nil {
		l.writeJSON(prefix+"_response.json", map[string]any{
			"error": err.Error(),
		})
	} else {
		l.writeJSON(prefix+"_response.json", resp)
	}

	return resp, err
}

func (l *LoggingClient) writeJSON(filename string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to marshal log entry %s: %v\n", filename, err)
		return
	}
	path := filepath.Join(l.logDir, filename)
	if err := os.MkdirAll(l.logDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to create log dir %s: %v\n", l.logDir, err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write log %s: %v\n", path, err)
	}
}
