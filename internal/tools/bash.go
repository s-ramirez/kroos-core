package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"
	"time"
)

type BashTool struct {
	// DefaultTimeout applies when the call sets no timeoutMs (0 = none).
	DefaultTimeout time.Duration
}

var bashDefinition = ToolDefinition{
	Name:        "bash",
	Description: "Execute a bash command in the working directory. Returns stdout, stderr, exit code, and whether the process was killed.",
	Guidelines: []string{
		"Use bash when you need to run shell commands, pipelines, or scripts.",
		"Set cwd when the command should run somewhere other than the current working directory.",
	},
	Parameters: JSONSchema{
		"type": "object",
		"properties": map[string]any{
			"command":   map[string]any{"type": "string", "description": "Bash command to execute"},
			"cwd":       map[string]any{"type": "string", "description": "Working directory for the command"},
			"timeoutMs": map[string]any{"type": "number", "description": "Optional timeout in milliseconds"},
		},
		"required": []string{"command"},
	},
	SelectionRules: []SelectionRule{
		{Text: "Use bash to run shell commands when you need command output, tests, or build results."},
		{Text: "Prefer bash over guessing command results."},
	},
}

type BashParams struct {
	Command   string `json:"command"`
	Cwd       string `json:"cwd"`
	TimeoutMS int    `json:"timeoutMs"`
}

type shellResult struct {
	Stdout string
	Stderr string
	Code   int
	Killed bool
}

func NewBashTool() *BashTool {
	return &BashTool{}
}

// NewBashToolWithTimeout returns a bash tool whose commands are killed after
// d unless the call asks for a different timeoutMs (for unattended agents).
func NewBashToolWithTimeout(d time.Duration) *BashTool {
	return &BashTool{DefaultTimeout: d}
}

func (t *BashTool) Definition() ToolDefinition {
	return bashDefinition
}

func (t *BashTool) Execute(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var params BashParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult("invalid JSON arguments"), nil
	}
	if params.Command == "" {
		return errorResult("command is required"), nil
	}
	if params.TimeoutMS < 0 {
		return errorResult("timeoutMs must be greater than or equal to 0"), nil
	}

	cwd := params.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return errorResultf("failed to resolve working directory: %v", err), nil
		}
	}

	timeout := time.Duration(params.TimeoutMS) * time.Millisecond
	if timeout == 0 {
		timeout = t.DefaultTimeout
	}
	result, err := runShell(ctx, params.Command, cwd, timeout)
	if err != nil {
		return errorResultf("failed to execute command: %v", err), nil
	}

	isError := result.Killed || result.Code != 0
	return ToolResult{
		Output:  formatBashOutput(cwd, result),
		IsError: isError,
	}, nil
}

func runShell(ctx context.Context, command string, cwd string, timeout time.Duration) (shellResult, error) {
	if command == "" {
		return shellResult{}, errors.New("command is required")
	}

	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := osexec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir = cwd

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := shellResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Code:   shellExitCode(cmd.ProcessState, err),
		Killed: runCtx.Err() != nil,
	}

	if err == nil {
		return result, nil
	}

	var exitErr *osexec.ExitError
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &exitErr) {
		return result, nil
	}

	return result, err
}

func shellExitCode(state *os.ProcessState, err error) int {
	if state != nil {
		return state.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func emptyMarker(value string) string {
	if value == "" {
		return "(empty)"
	}
	return value
}

func formatBashOutput(cwd string, result shellResult) string {
	sections := []string{
		fmt.Sprintf("cwd: %s", cwd),
		fmt.Sprintf("exit_code: %d", result.Code),
		fmt.Sprintf("killed: %t", result.Killed),
		"stdout:",
		emptyMarker(result.Stdout),
		"stderr:",
		emptyMarker(result.Stderr),
	}

	return strings.Join(sections, "\n")
}
