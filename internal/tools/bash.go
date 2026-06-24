// Package tools defines the MCP tool implementations for bezalel.
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/aleksclark/bezalel/internal/shell"
)

// BashParams are the parameters for the bash tool.
type BashParams struct {
	Command         string `json:"command"`
	Description     string `json:"description,omitempty"`
	WorkingDir      string `json:"working_dir,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

// BashResult is the response from the bash tool.
type BashResult struct {
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Background bool   `json:"background,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	WorkingDir string `json:"working_dir,omitempty"`
}

// JobOutputParams are the parameters for the job_output tool.
type JobOutputParams struct {
	JobID string `json:"job_id"`
}

// JobKillParams are the parameters for the job_kill tool.
type JobKillParams struct {
	JobID string `json:"job_id"`
}

// Toolbox holds all tool implementations and their shared state.
type Toolbox struct {
	shellMgr *shell.Manager
}

// NewToolbox creates a new toolbox with the given working directory.
func NewToolbox(workingDir string) *Toolbox {
	return &Toolbox{
		shellMgr: shell.NewManager(workingDir),
	}
}

// Shutdown cleans up all resources.
func (t *Toolbox) Shutdown() {
	t.shellMgr.KillAll()
}

// ExecBash executes a shell command.
func (t *Toolbox) ExecBash(ctx context.Context, params BashParams) (*BashResult, error) {
	if params.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	t.shellMgr.Cleanup()

	if params.RunInBackground {
		job, err := t.shellMgr.ExecBackground(ctx, params.Command, params.WorkingDir, params.Description)
		if err != nil {
			return nil, fmt.Errorf("failed to start background job: %w", err)
		}

		// Wait briefly to detect fast failures
		time.Sleep(1 * time.Second)
		stdout, stderr, done, execErr := job.GetOutput()

		if done {
			_ = t.shellMgr.KillJob(job.ID)
			output := formatOutput(stdout, stderr, execErr)
			return &BashResult{
				Output:     output,
				ExitCode:   shell.ExitCodeFromError(execErr),
				WorkingDir: job.WorkingDir,
			}, nil
		}

		return &BashResult{
			Output:     fmt.Sprintf("Background job started with ID: %s\n\nUse job_output to view output or job_kill to terminate.", job.ID),
			Background: true,
			JobID:      job.ID,
			WorkingDir: job.WorkingDir,
		}, nil
	}

	// Synchronous execution with auto-background
	result, job, err := t.shellMgr.Exec(ctx, params.Command, params.WorkingDir, params.Description)
	if err != nil {
		return nil, err
	}

	if job != nil {
		// Promoted to background
		return &BashResult{
			Output:     fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground job ID: %s\n\nUse job_output to view output or job_kill to terminate.", job.ID),
			Background: true,
			JobID:      job.ID,
			WorkingDir: job.WorkingDir,
		}, nil
	}

	output := formatOutput(result.Stdout, result.Stderr, nil)
	if result.ExitCode != 0 {
		output += fmt.Sprintf("\nExit code %d", result.ExitCode)
	}

	return &BashResult{
		Output:     output,
		ExitCode:   result.ExitCode,
		WorkingDir: params.WorkingDir,
	}, nil
}

// GetJobOutput retrieves the current output of a background job.
func (t *Toolbox) GetJobOutput(ctx context.Context, params JobOutputParams) (string, error) {
	if params.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}

	job, ok := t.shellMgr.GetJob(params.JobID)
	if !ok {
		return "", fmt.Errorf("job not found: %s", params.JobID)
	}

	stdout, stderr, done, err := job.GetOutput()

	var status string
	if done {
		status = "completed"
		if err != nil {
			exitCode := shell.ExitCodeFromError(err)
			if exitCode != 0 {
				stderr += fmt.Sprintf("\nExit code %d", exitCode)
			}
		}
	} else {
		status = "running"
	}

	output := formatOutput(stdout, stderr, nil)
	if output == "" {
		output = "no output"
	}

	return fmt.Sprintf("Status: %s\n\n%s", status, output), nil
}

// KillJob terminates a background job.
func (t *Toolbox) KillJob(ctx context.Context, params JobKillParams) (string, error) {
	if params.JobID == "" {
		return "", fmt.Errorf("job_id is required")
	}

	err := t.shellMgr.KillJob(params.JobID)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Background job %s terminated successfully", params.JobID), nil
}

func formatOutput(stdout, stderr string, err error) string {
	stdout = shell.TruncateOutput(stdout)
	stderr = shell.TruncateOutput(stderr)

	if stdout == "" && stderr == "" {
		return "no output"
	}

	var output string
	if stdout != "" {
		output = stdout
	}
	if stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += stderr
	}
	return output
}
