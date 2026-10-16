package imessage

import "testing"

func TestStripMarkdown(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Hello, world!", "Hello, world!"},
		{"**bold**", "bold"},
		{"__bold__", "bold"},
		{"*italic*", "italic"},
		{"_italic_", "italic"},
		{"~~strike~~", "strike"},
		{"# Header", "Header"},
		{"### Deep header", "Deep header"},
		{"> blockquote", "blockquote"},
		{"`inline code`", "inline code"},
		{"---", ""},
		{"***", ""},
		{"___", ""},
		{"**bold** and *italic*", "bold and italic"},
		{"First\n\n\n\nSecond", "First\n\nSecond"},
		{"  spaces  ", "spaces"},
	}
	for _, tt := range tests {
		got := StripMarkdown(tt.in)
		if got != tt.want {
			t.Errorf("StripMarkdown(%q)\n  got  %q\n  want %q", tt.in, got, tt.want)
		}
	}
}
