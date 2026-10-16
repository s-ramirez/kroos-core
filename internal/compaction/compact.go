package compaction

import (
	"context"
	"fmt"

	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/types"
)

// Settings controls when and how compaction runs.
type Settings struct {
	Enabled          bool
	ReserveTokens    int
	KeepRecentTokens int
}

// DefaultSettings mirror Pi SDK's DEFAULT_COMPACTION_SETTINGS.
var DefaultSettings = Settings{
	Enabled:          true,
	ReserveTokens:    16384,
	KeepRecentTokens: 20000,
}

// CompactionSummaryPrefix is ported verbatim from Pi SDK messages.ts so
// the model recognizes the synthetic boundary.
const CompactionSummaryPrefix = `The conversation history before this point was compacted into the following summary:

<summary>
`

// CompactionSummarySuffix closes the prefix's <summary> tag.
const CompactionSummarySuffix = "\n</summary>"

// ShouldCompact returns true when the current context usage has crossed
// the compaction threshold defined by Settings.
func ShouldCompact(contextTokens, contextWindow int, s Settings) bool {
	if !s.Enabled {
		return false
	}
	if contextWindow <= 0 {
		return false
	}
	return contextTokens > contextWindow-s.ReserveTokens
}

// Result is returned by Compact so callers can log before/after token counts.
type Result struct {
	Messages     []types.Message
	Summary      string
	TokensBefore int
	CutIndex     int
}

// Compact trims the oldest portion of messages and replaces it with a
// single synthetic user message carrying an LLM-generated summary.
//
// The first message (assumed to be the system prompt) is preserved. If
// there is nothing substantial to compact, Compact returns the original
// slice unchanged and an empty summary.
func Compact(
	ctx context.Context,
	client llm.ChatClient,
	messages []types.Message,
	previousSummary string,
	settings Settings,
	tokensBefore int,
) (Result, error) {
	if len(messages) < 2 {
		return Result{Messages: messages, TokensBefore: tokensBefore}, nil
	}

	cut := FindCutPoint(messages, 1, settings.KeepRecentTokens)
	if cut <= 1 || cut >= len(messages) {
		return Result{Messages: messages, TokensBefore: tokensBefore, CutIndex: cut}, nil
	}

	toSummarize := messages[1:cut]
	summary, err := Summarize(ctx, client, toSummarize, previousSummary)
	if err != nil {
		return Result{Messages: messages, TokensBefore: tokensBefore, CutIndex: cut}, err
	}

	synthetic := SyntheticSummaryMessage(summary)
	newMessages := make([]types.Message, 0, 2+len(messages)-cut)
	newMessages = append(newMessages, messages[0])
	newMessages = append(newMessages, synthetic)
	newMessages = append(newMessages, messages[cut:]...)

	return Result{
		Messages:     newMessages,
		Summary:      summary,
		TokensBefore: tokensBefore,
		CutIndex:     cut,
	}, nil
}

// SyntheticSummaryMessage wraps a summary in Pi's prefix/suffix so the
// model identifies it as a compaction boundary.
func SyntheticSummaryMessage(summary string) types.Message {
	return types.Message{
		Role:    "user",
		Content: CompactionSummaryPrefix + summary + CompactionSummarySuffix,
	}
}

// FormatTriggerLine produces a single-line log string for runner output.
func FormatTriggerLine(before, window int, s Settings) string {
	return fmt.Sprintf("[compacting: %d tokens, window=%d reserve=%d keep=%d]",
		before, window, s.ReserveTokens, s.KeepRecentTokens)
}
