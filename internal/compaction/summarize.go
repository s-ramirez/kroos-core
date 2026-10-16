package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/types"
)

// SummarizationSystemPrompt is ported verbatim from Pi SDK
// (packages/coding-agent/src/core/compaction/utils.ts).
const SummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI coding assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// SummarizationPrompt is ported verbatim from Pi SDK
// (packages/coding-agent/src/core/compaction/compaction.ts).
const SummarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// UpdateSummarizationPrompt merges fresh messages into an existing summary.
// Ported verbatim from Pi SDK.
const UpdateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const toolResultMaxChars = 2000

// Summarize calls the LLM once to produce a structured summary of the
// supplied conversation slice. previousSummary may be empty.
func Summarize(ctx context.Context, client llm.ChatClient, messages []types.Message, previousSummary string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("compaction: client is required")
	}
	if len(messages) == 0 {
		return "", fmt.Errorf("compaction: nothing to summarize")
	}

	conversationText := SerializeConversation(messages)

	var body strings.Builder
	body.WriteString("<conversation>\n")
	body.WriteString(conversationText)
	body.WriteString("\n</conversation>\n\n")
	if previousSummary != "" {
		body.WriteString("<previous-summary>\n")
		body.WriteString(previousSummary)
		body.WriteString("\n</previous-summary>\n\n")
		body.WriteString(UpdateSummarizationPrompt)
	} else {
		body.WriteString(SummarizationPrompt)
	}

	req := []types.Message{
		{Role: "system", Content: SummarizationSystemPrompt},
		{Role: "user", Content: body.String()},
	}

	resp, err := client.Chat(ctx, req, nil)
	if err != nil {
		return "", fmt.Errorf("compaction: summarization chat failed: %w", err)
	}
	text := strings.TrimSpace(types.GetResponseText(resp))
	if text == "" {
		return "", fmt.Errorf("compaction: summarization returned empty text")
	}
	return text, nil
}

// SerializeConversation flattens messages to plain text so the summarizer
// does not try to continue them. Tool results are truncated to keep the
// request small.
func SerializeConversation(messages []types.Message) string {
	var parts []string
	for i := range messages {
		m := &messages[i]
		switch m.Role {
		case "system":
			continue
		case "user":
			if s := textContent(m.Content); s != "" {
				parts = append(parts, "[User]: "+s)
			}
		case "assistant":
			if s := textContent(m.Content); s != "" {
				parts = append(parts, "[Assistant]: "+s)
			}
			if len(m.ToolCalls) > 0 {
				calls := make([]string, 0, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					calls = append(calls, fmt.Sprintf("%s(%s)", tc.Function.Name, tc.Function.Arguments))
				}
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(calls, "; "))
			}
		case "tool":
			if s := textContent(m.Content); s != "" {
				parts = append(parts, "[Tool result]: "+truncateForSummary(s, toolResultMaxChars))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func textContent(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, block := range v {
			if bm, ok := block.(map[string]any); ok {
				if t, _ := bm["type"].(string); t == "text" {
					if s, _ := bm["text"].(string); s != "" {
						b.WriteString(s)
					}
				}
			}
		}
		return b.String()
	default:
		if raw, err := json.Marshal(v); err == nil {
			return string(raw)
		}
		return ""
	}
}

func truncateForSummary(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	head := maxChars - 100
	if head < 1 {
		head = maxChars
	}
	return s[:head] + fmt.Sprintf("\n...[truncated %d chars]", len(s)-head)
}
