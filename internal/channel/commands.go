package channel

import (
	"fmt"
	"os"
	"strings"

	"github.com/s-ramirez/kroos-core/internal/skills"
)

const (
	SkillPrefix          = "/skill:"
	MemoryDistillCommand = "/memory:distill"
)

// ParseResetCommand reports whether input begins with /new or /reset
// (followed by whitespace or end-of-input) and returns any trailing
// body. The tail has surrounding whitespace trimmed. A trailing body
// means "clear the conversation, then treat this as the first user
// message of the fresh session".
func ParseResetCommand(input string) (matched bool, tail string) {
	for _, cmd := range []string{"/new", "/reset"} {
		if input == cmd {
			return true, ""
		}
		rest, ok := strings.CutPrefix(input, cmd)
		if !ok {
			continue
		}
		if rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' {
			return true, strings.TrimSpace(rest)
		}
	}
	return false, ""
}

// DistillInstruction is the fixed prompt used by both the manual
// /memory:distill slash command and the auto-distill flow at session
// exit. Exported so non-dispatcher callers (DistillMemory) can reuse it.
const DistillInstruction = `Review this session and refresh long-term memory.

1. Call memory_read with path="MEMORY.md" to see current curated memory. A missing file is fine.
2. Call memory_list with dir="sessions" to see available session notes. Read recent ones that look relevant with memory_read.
3. Decide whether this session produced anything worth preserving long-term: user preferences, active priorities, hard-won lessons, durable facts. Skip anything already in the agent config, skills, or obvious from project files.
4. If there is something worth updating, call memory_write with path="MEMORY.md", mode="overwrite", and content containing a refreshed MEMORY.md (aim for under 3000 characters, plain Markdown, no frontmatter required). Then reply with one short sentence summarizing what changed.
5. If nothing needs updating, reply with exactly: NO_UPDATE

Do not ask the user questions — this is a background review.`

// CommandExpander rewrites slash-command inputs into the prompts the
// model actually sees. Unknown input is returned unchanged so regular
// messages pass through untouched.
type CommandExpander struct {
	Skills *skills.Registry
}

func (e *CommandExpander) Expand(input string) (string, error) {
	if input == MemoryDistillCommand {
		return DistillInstruction, nil
	}
	if !strings.HasPrefix(input, SkillPrefix) {
		return input, nil
	}

	rest := input[len(SkillPrefix):]
	name := rest
	args := ""
	if idx := strings.IndexByte(rest, ' '); idx >= 0 {
		name = rest[:idx]
		args = strings.TrimSpace(rest[idx+1:])
	}

	for _, s := range e.Skills.Snapshot() {
		if s.Name == name {
			content, err := os.ReadFile(s.FilePath)
			if err != nil {
				return "", fmt.Errorf("reading skill %q: %w", name, err)
			}
			result := string(content)
			if args != "" {
				result += "\n\n" + args
			}
			return result, nil
		}
	}

	return "", fmt.Errorf("unknown skill %q", name)
}
