package compaction

import (
	"encoding/json"

	"github.com/s-ramirez/kroos-core/internal/types"
)

// EstimateTokens returns a conservative chars/4 estimate for a single message.
// Ported from Pi SDK's estimateTokens (packages/coding-agent/src/core/compaction/compaction.ts).
func EstimateTokens(m types.Message) int {
	chars := contentChars(m.Content)
	for _, tc := range m.ToolCalls {
		chars += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return (chars + 3) / 4
}

func contentChars(content any) int {
	switch v := content.(type) {
	case nil:
		return 0
	case string:
		return len(v)
	case []any:
		total := 0
		for _, block := range v {
			if b, ok := block.(map[string]any); ok {
				if t, _ := b["type"].(string); t == "text" {
					if s, _ := b["text"].(string); s != "" {
						total += len(s)
					}
				} else if t == "image" || t == "image_url" {
					total += 4800
				}
			}
		}
		return total
	default:
		// Fallback: encode to JSON and measure.
		if raw, err := json.Marshal(v); err == nil {
			return len(raw)
		}
		return 0
	}
}

// EstimateContextTokens returns the total context token count. If lastUsage
// is provided, its TotalTokens is taken as authoritative for the prefix up
// through lastUsageIndex (inclusive) and only trailing messages are estimated.
// Pass lastUsage=nil to estimate every message from scratch.
func EstimateContextTokens(messages []types.Message, lastUsage *types.Usage, lastUsageIndex int) int {
	if lastUsage == nil || lastUsageIndex < 0 || lastUsageIndex >= len(messages) {
		total := 0
		for i := range messages {
			total += EstimateTokens(messages[i])
		}
		return total
	}
	total := lastUsage.TotalTokens
	for i := lastUsageIndex + 1; i < len(messages); i++ {
		total += EstimateTokens(messages[i])
	}
	return total
}
