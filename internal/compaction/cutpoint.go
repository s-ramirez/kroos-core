package compaction

import "github.com/s-ramirez/kroos-core/internal/types"

// FindCutPoint returns the index of the first message to KEEP after
// compaction. Everything in [startIndex, cut) is fed to the summarizer;
// messages[cut:] is preserved verbatim.
//
// Algorithm (ported from Pi SDK):
//  1. Walk backwards from newest, accumulating chars/4 estimates.
//  2. Once the accumulated count crosses keepRecentTokens, snap forward
//     to the next valid cut point.
//
// Valid cut points are user or assistant messages. Tool messages can never
// be cut points: they must follow their originating assistant tool_call.
// If the cut would land inside an assistant→tool pair, bump forward until
// the kept range is self-contained.
//
// startIndex lets callers skip the system prompt (pass 1).
func FindCutPoint(messages []types.Message, startIndex, keepRecentTokens int) int {
	if startIndex < 0 {
		startIndex = 0
	}
	if startIndex >= len(messages) {
		return len(messages)
	}

	cutPoints := validCutPoints(messages, startIndex)
	if len(cutPoints) == 0 {
		return len(messages)
	}

	accumulated := 0
	cut := cutPoints[len(cutPoints)-1]
	for i := len(messages) - 1; i >= startIndex; i-- {
		accumulated += EstimateTokens(messages[i])
		if accumulated >= keepRecentTokens {
			for _, cp := range cutPoints {
				if cp >= i {
					cut = cp
					break
				}
			}
			break
		}
	}

	return ensureSelfContained(messages, cut)
}

func validCutPoints(messages []types.Message, startIndex int) []int {
	out := make([]int, 0, len(messages)-startIndex)
	for i := startIndex; i < len(messages); i++ {
		switch messages[i].Role {
		case "user", "assistant":
			out = append(out, i)
		}
	}
	return out
}

// ensureSelfContained walks the cut forward so the kept slice does not
// contain orphan tool messages whose assistant call was dropped.
func ensureSelfContained(messages []types.Message, cut int) int {
	for cut < len(messages) && messages[cut].Role == "tool" {
		cut++
	}
	return cut
}
