package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/skills"
	"github.com/s-ramirez/kroos-core/internal/tasks"
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

// CuratorAgent is the tasks.Task Agent kind for the skill curator.
const CuratorAgent = "curator"

// CuratorPrompt drives the consolidation pass. Adapted from
// hermes-agent's curator.
const CuratorPrompt = `You are the background skill CURATOR. This is a consolidation pass over agent-created skills, not a passive audit.

Goal: a library of CLASS-LEVEL skills. Hundreds of narrow skills that each capture one session's bug is a failure; one broad skill with labeled subsections beats five narrow siblings, because routing matches on descriptions.

Hard rules:
- Only touch the candidates listed below (enforced). You may create a new umbrella skill.
- Never delete. Archiving (skill_manage action=archive, sole op per call) is the most destructive action and is recoverable.
- Usage counters are not a reason to keep or prune: judge on content. Never archive a skill younger than 30 days unless it was fully absorbed elsewhere.
- Do not reject a merge because each skill has a distinct trigger. The bar is: would a maintainer write these as N skills or as one skill with N subsections?

How to work:
1. Scan the candidates for clusters (shared prefix or domain).
2. For each cluster of 2+, pick or create the umbrella. skill_view each member (required before patching), then patch the umbrella: distill absorbed content into rules (imperative + why), merge duplicate lessons, drop narration/dates. Broaden its description so every absorbed trigger still routes ("Use when ...", trigger first).
3. Put topical depth in references/<topic>.md under the umbrella (extend an existing file before adding one) with a one-line pointer in SKILL.md. Moving a file unchanged is filing, not consolidating.
4. Archive each absorbed sibling after its content lives in the umbrella.
5. Any SKILL.md body over ~24k chars should be distilled to always-on rules with depth pushed into references/.

If nothing overlaps, reply NOTHING_TO_REPORT. Otherwise reply with one short paragraph listing merges and archives.`

// CuratorConfig configures the curator's lifecycle and scope.
type CuratorConfig struct {
	Usage        *skills.Usage
	Registry     *skills.Registry
	ArchiveDir   string
	StaleAfter   time.Duration
	ArchiveAfter time.Duration
	Reload       func() error
}

// BuildCurator applies the code-only lifecycle (stale/archive by idle
// time), then returns an agent for the LLM consolidation pass over the
// remaining agent-created, unpinned skills. With fewer than two
// candidates it returns a no-op agent.
func BuildCurator(client llm.ChatClient, skillTools []tools.Tool, cfg CuratorConfig) (tasks.AgentSpec, error) {
	res, err := cfg.Usage.ApplyLifecycle(cfg.Registry.Snapshot(), cfg.ArchiveDir, cfg.StaleAfter, cfg.ArchiveAfter)
	if err != nil {
		return tasks.AgentSpec{}, err
	}
	if len(res.Archived) > 0 && cfg.Reload != nil {
		if err := cfg.Reload(); err != nil {
			return tasks.AgentSpec{}, err
		}
	}
	lifecycle := describeLifecycle(res)

	entries := cfg.Usage.Load()
	var candidates []skills.Skill
	scope := map[string]bool{}
	for _, s := range cfg.Registry.Snapshot() {
		e := entries[s.Name]
		if e.CreatedBy == skills.CreatedByAgent && !e.Pinned {
			candidates = append(candidates, s)
			scope[s.Name] = true
		}
	}
	if len(candidates) < 2 {
		reply := tasks.NothingToReport
		if lifecycle != "" {
			reply = lifecycle
		}
		return tasks.AgentSpec{Agent: staticAgent(reply), Prompt: "lifecycle only"}, nil
	}

	reg := tools.NewRegistry(skillTools...)
	hook := newReadBeforeWriteHook()
	hook.allowArchive = true
	hook.scope = scope
	reg.AddHook(hook)
	r := &Runner{
		Client:    client,
		Registry:  reg,
		Skills:    cfg.Registry,
		AgentID:   CuratorAgent,
		AgentName: CuratorAgent,
		Prompt:    "You are kroos's background skill curator.",
	}
	return tasks.AgentSpec{
		Agent:   r,
		System:  r.SystemPrompt(),
		Schemas: reg.Schemas(),
		Prompt:  CuratorPrompt + "\n\n" + lifecycleNote(lifecycle) + describeCandidates(candidates, entries, time.Now()),
	}, nil
}

func describeLifecycle(res skills.LifecycleResult) string {
	var parts []string
	if len(res.Archived) > 0 {
		parts = append(parts, "archived idle skills: "+strings.Join(res.Archived, ", "))
	}
	if len(res.Staled) > 0 {
		parts = append(parts, "marked stale: "+strings.Join(res.Staled, ", "))
	}
	return strings.Join(parts, "; ")
}

func lifecycleNote(lifecycle string) string {
	if lifecycle == "" {
		return ""
	}
	return "Lifecycle already applied this run: " + lifecycle + ".\n\n"
}

func describeCandidates(cands []skills.Skill, entries map[string]skills.UsageEntry, now time.Time) string {
	var b strings.Builder
	b.WriteString("<candidates>\n")
	for _, s := range cands {
		e := entries[s.Name]
		state := e.State
		if state == "" {
			state = skills.StateActive
		}
		age := "unknown"
		if e.CreatedAt != nil {
			age = fmt.Sprintf("%dd", int(now.Sub(*e.CreatedAt).Hours()/24))
		}
		fmt.Fprintf(&b, "- %s (state=%s, use=%d, age=%s, body=%d chars): %s\n", s.Name, state, e.UseCount, age, len(s.Content), s.Description)
	}
	b.WriteString("</candidates>")
	return b.String()
}

// staticAgent returns a fixed reply without calling the LLM.
type staticAgent string

func (a staticAgent) RunConversation(_ context.Context, m []types.Message, _ []types.ChatRequestTool) (string, []types.Message, error) {
	return string(a), m, nil
}
