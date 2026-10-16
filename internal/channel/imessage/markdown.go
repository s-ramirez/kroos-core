package imessage

import (
	"regexp"
	"strings"
)

var (
	reBold          = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	reItalic        = regexp.MustCompile(`\*(.+?)\*|_(.+?)_`)
	reStrikethrough = regexp.MustCompile(`~~(.+?)~~`)
	reHeader        = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reBlockquote    = regexp.MustCompile(`(?m)^>\s?`)
	reHR            = regexp.MustCompile(`(?m)^(\*{3,}|-{3,}|_{3,})\s*$`)
	reInlineCode    = regexp.MustCompile("`(.+?)`")
	reExtraNewlines = regexp.MustCompile(`\n{3,}`)
)

// StripMarkdown removes common Markdown formatting that would render as
// literal punctuation in iMessage. The output is plain text suitable for
// sending via the BlueBubbles API.
func StripMarkdown(s string) string {
	// Remove horizontal rules entirely.
	s = reHR.ReplaceAllString(s, "")
	// Strip header sigils (### Title → Title).
	s = reHeader.ReplaceAllString(s, "")
	// Strip blockquote markers.
	s = reBlockquote.ReplaceAllString(s, "")
	// Bold before italic so **x** is not partially matched by italic.
	s = reBold.ReplaceAllStringFunc(s, func(m string) string {
		groups := reBold.FindStringSubmatch(m)
		if groups[1] != "" {
			return groups[1]
		}
		return groups[2]
	})
	s = reItalic.ReplaceAllStringFunc(s, func(m string) string {
		groups := reItalic.FindStringSubmatch(m)
		if groups[1] != "" {
			return groups[1]
		}
		return groups[2]
	})
	s = reStrikethrough.ReplaceAllStringFunc(s, func(m string) string {
		return reStrikethrough.FindStringSubmatch(m)[1]
	})
	s = reInlineCode.ReplaceAllStringFunc(s, func(m string) string {
		return reInlineCode.FindStringSubmatch(m)[1]
	})
	// Collapse runs of 3+ newlines down to 2.
	s = reExtraNewlines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
