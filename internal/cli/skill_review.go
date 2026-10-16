package cli

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/s-ramirez/kroos-core/internal/llm"
	"github.com/s-ramirez/kroos-core/internal/tasks"
	"github.com/s-ramirez/kroos-core/internal/tools"
	"github.com/s-ramirez/kroos-core/internal/types"
)

const skillReviewTimeout = 5 * time.Minute

// SkillReviewPrompt asks a forked agent to fold the conversation's lessons
// into the skill library. Adapted from hermes-agent's background review.
const SkillReviewPrompt = `Review the conversation above and update the skill library (skill_view / skill_manage only; you are a background pass, the user will not see your reply, and other tools will fail).

Target shape: CLASS-LEVEL skills, each a SKILL.md of always-on rules plus a small references/ set of topical depth. Not a flat list of narrow one-session skills.

Signals (any one warrants action):
- The user corrected your style, tone, format, verbosity, or said "remember this" / "stop doing X". Embed the preference in the skill governing that task.
- The user corrected your workflow or sequence of steps. Encode it as a step or pitfall.
- A non-trivial technique, fix, workaround, or tool-usage pattern emerged that a future session would benefit from.
- A skill loaded this session was wrong, missing a step, or outdated. Patch it.

Preference order — pick the earliest that fits:
1. Patch a skill that was loaded this session.
2. Patch an existing broader skill that covers the class (check <available_skills>).
3. Add references/<topic>.md (or templates/, scripts/) under an existing skill and add a one-line pointer in its SKILL.md. Name by topic; extend an existing file before adding one.
4. Create a new class-level skill. Never name it after a PR, error string, or today's task.

Rules:
- Call skill_view on a skill before patching it (enforced).
- Write lessons, not logs: imperative rule + why. No dates, transcripts, or incident narration.
- Never delete skills.
- Do NOT capture one-off task details, secrets, or facts about the user that belong in memory rather than in how-to procedures.

If the session ran smoothly with no corrections and no new technique, reply "Nothing to save." and stop. Otherwise act, then reply with one line summarizing what changed.`

type skillReviewState struct {
	pending atomic.Int64
}

// maybeReviewSkills accumulates tool calls and, once the interval is
// reached, submits a background review over a snapshot of the
// conversation. The executor keeps at most one review in flight.
func (r *Runner) maybeReviewSkills(messages []types.Message, toolSchemas []types.ChatRequestTool, state *compactionState) {
	if r.SkillReviewInterval <= 0 || r.AgentID != "main" || r.Tasks == nil {
		return
	}
	if state.usedSkillManage {
		r.skillReview.pending.Store(0)
		return
	}
	if r.skillReview.pending.Add(int64(state.toolCalls)) < int64(r.SkillReviewInterval) {
		return
	}
	snapshot := make([]types.Message, len(messages))
	copy(snapshot, messages)
	if _, ok := r.Tasks.Submit(tasks.Task{
		ID:      SkillReviewAgent,
		Name:    SkillReviewAgent,
		Agent:   SkillReviewAgent,
		Prompt:  SkillReviewPrompt,
		Context: snapshot,
		Schemas: toolSchemas,
		Timeout: skillReviewTimeout,
	}); ok {
		r.skillReview.pending.Store(0)
	}
}

// SkillReviewAgent is the tasks.Task Agent kind for skill reviews.
const SkillReviewAgent = "skill-review"

// NewSkillReviewRunner builds the review fork: a runner that can only
// dispatch reviewTools, with skill_view-before-patch enforced. Callers
// pass the main conversation's schemas so the request shares its
// prompt-cache prefix; other tool calls fail as unknown.
func NewSkillReviewRunner(client llm.ChatClient, reviewTools []tools.Tool) *Runner {
	reg := tools.NewRegistry(reviewTools...)
	reg.AddHook(newReadBeforeWriteHook())
	return &Runner{Client: client, Registry: reg, AgentID: SkillReviewAgent, AgentName: SkillReviewAgent}
}

// readBeforeWriteHook blocks review-fork patches to skills it has not
// loaded via skill_view during the review, and blocks deletes outright.
type readBeforeWriteHook struct {
	viewed map[string]bool
}

func newReadBeforeWriteHook() *readBeforeWriteHook {
	return &readBeforeWriteHook{viewed: map[string]bool{}}
}

func viewKey(name, file string) string {
	if file == "" {
		file = "SKILL.md"
	}
	return name + "/" + file
}

func (h *readBeforeWriteHook) BeforeToolCall(_ tools.HookContext, name string, params json.RawMessage) string {
	if name != "skill_manage" {
		return ""
	}
	var p tools.SkillManageParams
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	for _, op := range p.Operations {
		switch op.Action {
		case "delete":
			return "background review must not delete skills"
		case "patch", "remove_file":
			if !h.viewed[viewKey(op.Name, op.FilePath)] {
				return fmt.Sprintf("call skill_view(name=%q, file_path=%q) first, then retry once", op.Name, op.FilePath)
			}
		}
	}
	return ""
}

func (h *readBeforeWriteHook) AfterToolCall(_ tools.HookContext, name string, params json.RawMessage, result tools.ToolResult, err error) {
	if err != nil || result.IsError {
		return
	}
	switch name {
	case "skill_view":
		var p tools.SkillViewParams
		if json.Unmarshal(params, &p) == nil {
			h.viewed[viewKey(p.Name, p.FilePath)] = true
		}
	case "skill_manage":
		// Content the fork just wrote counts as read.
		var p tools.SkillManageParams
		if json.Unmarshal(params, &p) == nil {
			for _, op := range p.Operations {
				h.viewed[viewKey(op.Name, op.FilePath)] = true
			}
		}
	}
}
