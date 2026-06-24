package shell_test

import (
	"context"
	"testing"
	"time"

	"github.com/aleksclark/bezalel/internal/shell"
)

func TestExecSynchronous(t *testing.T) {
	mgr := shell.NewManager(t.TempDir())

	result, job, err := mgr.Exec(context.Background(), "echo hello", "", "test echo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job != nil {
		t.Fatal("expected synchronous result, got background job")
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Fatalf("expected 'hello\\n', got %q", result.Stdout)
	}
}

func TestExecNonZeroExit(t *testing.T) {
	mgr := shell.NewManager(t.TempDir())

	result, _, err := mgr.Exec(context.Background(), "exit 42", "", "test exit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", result.ExitCode)
	}
}

func TestExecBackground(t *testing.T) {
	mgr := shell.NewManager(t.TempDir())

	job, err := mgr.ExecBackground(context.Background(), "echo bg-test", "", "background test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if job.ID == "" {
		t.Fatal("expected job ID")
	}

	// Wait for completion
	deadline := time.Now().Add(5 * time.Second)
	for !job.IsDone() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}

	if !job.IsDone() {
		t.Fatal("job did not complete in time")
	}

	stdout, _, _, _ := job.GetOutput()
	if stdout != "bg-test\n" {
		t.Fatalf("expected 'bg-test\\n', got %q", stdout)
	}
}

func TestKillJob(t *testing.T) {
	mgr := shell.NewManager(t.TempDir())

	job, err := mgr.ExecBackground(context.Background(), "sleep 60", "", "long sleep")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = mgr.KillJob(job.ID)
	if err != nil {
		t.Fatalf("failed to kill job: %v", err)
	}

	// Should not find it anymore
	_, ok := mgr.GetJob(job.ID)
	if ok {
		t.Fatal("job should be removed after kill")
	}
}

func TestTruncateOutput(t *testing.T) {
	short := "hello world"
	if shell.TruncateOutput(short) != short {
		t.Fatal("short output should not be truncated")
	}

	// Generate output larger than MaxOutputLength
	long := make([]byte, shell.MaxOutputLength+1000)
	for i := range long {
		long[i] = 'x'
		if i%80 == 79 {
			long[i] = '\n'
		}
	}
	truncated := shell.TruncateOutput(string(long))
	if len(truncated) > shell.MaxOutputLength+200 { // some slack for the truncation message
		t.Fatalf("output not properly truncated: len=%d", len(truncated))
	}
}
