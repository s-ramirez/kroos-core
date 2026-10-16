package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sender policies for a profile binding.
const (
	SendersOwners = "owners" // only the channel's configured owners
	SendersAny    = "any"    // anyone in the bound group threads (mention/tag still required; DMs stay owner-only)
)

// PublicSafeTools is the closed set of tools a profile reachable by
// non-owners (senders: any) may use. Anything that touches the host
// filesystem, runs commands, sends messages elsewhere, or schedules
// work is excluded so untrusted senders cannot escalate.
var PublicSafeTools = map[string]bool{
	"web_search": true,
	"web_fetch":  true,
	"memory":     true,
}

// Profile is a top-level agent bound to specific channel threads. It
// reuses the Definition shape (model, tools, prompt) and adds routing.
// An empty Model means "same as the main agent".
type Profile struct {
	Definition
	Channel string   // "discord", "imessage" or "telegram"
	Threads []string // channel-specific thread IDs (Discord channel IDs, iMessage chat GUIDs, Telegram chat IDs)
	Senders string   // SendersOwners or SendersAny
}

// LoadProfiles loads profile definitions (*.md) from dirs. First profile
// with a given name wins; a thread bound by an earlier profile cannot be
// rebound. Invalid profiles are skipped with a diagnostic.
func LoadProfiles(dirs ...string) ([]Profile, []string, error) {
	var profiles []Profile
	var diagnostics []string
	seenName := make(map[string]string)
	seenThread := make(map[string]string)

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			p, diags, err := loadProfileFile(path)
			if err != nil {
				return nil, nil, err
			}
			diagnostics = append(diagnostics, diags...)
			if p == nil {
				continue
			}
			if existing, ok := seenName[p.Name]; ok {
				diagnostics = append(diagnostics,
					fmt.Sprintf("profile %q from %s ignored: already loaded from %s", p.Name, path, existing))
				continue
			}
			conflict := ""
			for _, t := range p.Threads {
				if owner, ok := seenThread[p.Channel+"/"+t]; ok {
					conflict = fmt.Sprintf("profile %q from %s ignored: %s thread %s already bound to %q", p.Name, path, p.Channel, t, owner)
					break
				}
			}
			if conflict != "" {
				diagnostics = append(diagnostics, conflict)
				continue
			}
			seenName[p.Name] = path
			for _, t := range p.Threads {
				seenThread[p.Channel+"/"+t] = p.Name
			}
			profiles = append(profiles, *p)
		}
	}
	return profiles, diagnostics, nil
}

func loadProfileFile(path string) (*Profile, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	fm, body := parseFrontmatter(string(data))

	name := strings.TrimSpace(fm["name"])
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	var diags []string
	bad := func(format string, args ...any) {
		diags = append(diags, fmt.Sprintf("%s: "+format, append([]any{path}, args...)...))
	}

	if len(name) > maxAgentNameLen || !agentNamePattern.MatchString(name) || name == "main" {
		bad("name %q must be alphanumeric with hyphens (max %d chars, not \"main\")", name, maxAgentNameLen)
	}

	ch := strings.TrimSpace(fm["channel"])
	if ch != "discord" && ch != "imessage" && ch != "telegram" {
		bad("channel must be \"discord\", \"imessage\" or \"telegram\", got %q", ch)
	}
	threads := parseList(fm["threads"])
	if len(threads) == 0 {
		bad("threads must list at least one thread ID")
	}

	senders := strings.TrimSpace(fm["senders"])
	if senders == "" {
		senders = SendersOwners
	}
	switch senders {
	case SendersOwners, SendersAny:
	default:
		bad("senders must be %q or %q, got %q", SendersOwners, SendersAny, senders)
	}

	tools := parseList(fm["tools"])
	if len(tools) == 0 {
		bad("tools must contain at least one tool name")
	}
	if senders == SendersAny {
		for _, t := range tools {
			if !PublicSafeTools[t] {
				bad("tool %q not allowed with senders: any (allowed: web_search, web_fetch, memory)", t)
			}
		}
	}

	contextSize, ctxErr := parseContextSizeField(fm["context_size"])
	if ctxErr != nil {
		bad("%v", ctxErr)
	}

	prompt := strings.TrimSpace(body)
	if prompt == "" {
		bad("prompt body is required")
	}

	if len(diags) > 0 {
		return nil, diags, nil
	}
	return &Profile{
		Definition: Definition{
			Name:        name,
			Description: strings.TrimSpace(fm["description"]),
			Model:       strings.TrimSpace(fm["model"]),
			Provider:    strings.TrimSpace(fm["provider"]),
			ContextSize: contextSize,
			Tools:       tools,
			Prompt:      prompt,
			FilePath:    path,
		},
		Channel: ch,
		Threads: threads,
		Senders: senders,
	}, nil, nil
}
