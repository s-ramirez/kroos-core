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

// BotCommand is a slash command kroos registers with chat platforms that
// have a command menu (Discord application commands, Telegram setMyCommands).
type BotCommand struct {
	Name        string // lowercase [a-z0-9_], valid on Discord and Telegram
	Description string
	Arg         string // optional free-text argument name ("" = none)
	ArgDesc     string
}

// BotCommands are the commands kroos understands as typed text; registering
// them replaces any commands left on the bot by other software.
var BotCommands = []BotCommand{
	{Name: "new", Description: "Start a new conversation", Arg: "message", ArgDesc: "Optional first message"},
	{Name: "model", Description: "List models or switch this conversation's model", Arg: "choice", ArgDesc: "Number, model name, provider:model, or default"},
	{Name: "doctor", Description: "Investigate and fix kroos problems", Arg: "note", ArgDesc: "What's wrong, or \"status\""},
	{Name: "skill", Description: "Run a skill", Arg: "name", ArgDesc: "Skill name followed by optional arguments"},
	{Name: "distill", Description: "Refresh long-term memory from this session"},
}

// CommandText is the text a slash command stands for ("/model 3").
func CommandText(name, arg string) string {
	return strings.TrimSpace("/" + name + " " + strings.TrimSpace(arg))
}

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
	// Usage, if set, records a use for each /skill: expansion so the
	// curator doesn't treat command-only skills as idle.
	Usage *skills.Usage
}

func (e *CommandExpander) Expand(input string) (string, error) {
	// Menu-friendly aliases (platform command names can't contain ':').
	if input == "/distill" {
		input = MemoryDistillCommand
	}
	if rest, ok := strings.CutPrefix(input, "/skill "); ok {
		input = SkillPrefix + strings.TrimSpace(rest)
	}
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
			_ = e.Usage.Bump(name) // best-effort telemetry
			result := string(content)
			if args != "" {
				result += "\n\n" + args
			}
			return result, nil
		}
	}

	return "", fmt.Errorf("unknown skill %q", name)
}

// parseSlash matches "/<name>" or "/<name> <args>".
func parseSlash(input, name string) (args string, ok bool) {
	input = strings.TrimSpace(input)
	if input == "/"+name {
		return "", true
	}
	rest, found := strings.CutPrefix(input, "/"+name+" ")
	if !found {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
