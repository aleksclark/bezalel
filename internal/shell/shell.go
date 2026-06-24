// Package shell provides command execution with background job management.
// Designed to match Crush's bash tool semantics: synchronous execution with
// automatic background promotion after a configurable threshold.
package shell

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// AutoBackgroundThreshold is the duration after which a foreground command
	// is automatically promoted to a background job.
	AutoBackgroundThreshold = 1 * time.Minute

	// MaxOutputLength is the maximum number of bytes returned in a command's output.
	MaxOutputLength = 30000

	// MaxBackgroundJobs is the maximum number of concurrent background jobs.
	MaxBackgroundJobs = 50

	// CompletedJobRetention is how long completed jobs are kept before cleanup.
	CompletedJobRetention = 8 * time.Hour
)

// Result holds the output of a completed command execution.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// BackgroundJob represents a shell command running in the background.
type BackgroundJob struct {
	ID          string
	Command     string
	Description string
	WorkingDir  string
	StartTime   time.Time

	ctx         context.Context
	cancel      context.CancelFunc
	stdout      *syncBuffer
	stderr      *syncBuffer
	done        chan struct{}
	exitErr     error
	completedAt atomic.Int64
}

// GetOutput returns the current output of a background job.
func (j *BackgroundJob) GetOutput() (stdout, stderr string, done bool, err error) {
	select {
	case <-j.done:
		return j.stdout.String(), j.stderr.String(), true, j.exitErr
	default:
		return j.stdout.String(), j.stderr.String(), false, nil
	}
}

// IsDone checks if the job has finished.
func (j *BackgroundJob) IsDone() bool {
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// Manager handles foreground and background command execution.
type Manager struct {
	workingDir string
	jobs       sync.Map // map[string]*BackgroundJob
	jobCount   atomic.Int64
	idCounter  atomic.Uint64
}

// NewManager creates a new shell manager.
func NewManager(workingDir string) *Manager {
	return &Manager{
		workingDir: workingDir,
	}
}

// WorkingDir returns the manager's working directory.
func (m *Manager) WorkingDir() string {
	return m.workingDir
}

// Exec runs a command synchronously. If the command takes longer than
// AutoBackgroundThreshold, it is promoted to a background job and the
// job ID is returned instead.
func (m *Manager) Exec(ctx context.Context, command, workingDir, description string) (*Result, *BackgroundJob, error) {
	if workingDir == "" {
		workingDir = m.workingDir
	}

	// Start the command as a background job so it can survive promotion
	job, err := m.startJob(ctx, command, workingDir, description)
	if err != nil {
		return nil, nil, err
	}

	// Wait for completion or timeout
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(AutoBackgroundThreshold)

	for {
		select {
		case <-ticker.C:
			if job.IsDone() {
				// Completed within threshold — return synchronously
				m.removeJob(job.ID)
				return m.jobToResult(job), nil, nil
			}
		case <-timeout:
			// Still running — promote to background
			return nil, job, nil
		case <-ctx.Done():
			_ = m.KillJob(job.ID)
			return nil, nil, ctx.Err()
		}
	}
}

// ExecBackground starts a command as a background job immediately.
func (m *Manager) ExecBackground(ctx context.Context, command, workingDir, description string) (*BackgroundJob, error) {
	if workingDir == "" {
		workingDir = m.workingDir
	}
	return m.startJob(ctx, command, workingDir, description)
}

// GetJob retrieves a background job by ID.
func (m *Manager) GetJob(id string) (*BackgroundJob, bool) {
	v, ok := m.jobs.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*BackgroundJob), true
}

// KillJob terminates a background job.
func (m *Manager) KillJob(id string) error {
	v, ok := m.jobs.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}
	job := v.(*BackgroundJob)
	job.cancel()
	<-job.done
	m.jobCount.Add(-1)
	return nil
}

// ListJobs returns all active job IDs.
func (m *Manager) ListJobs() []string {
	var ids []string
	m.jobs.Range(func(key, _ any) bool {
		ids = append(ids, key.(string))
		return true
	})
	return ids
}

// Cleanup removes completed jobs older than the retention period.
func (m *Manager) Cleanup() int {
	now := time.Now().Unix()
	retention := int64(CompletedJobRetention.Seconds())
	var removed int

	m.jobs.Range(func(key, value any) bool {
		job := value.(*BackgroundJob)
		completedAt := job.completedAt.Load()
		if completedAt > 0 && now-completedAt > retention {
			m.jobs.Delete(key)
			m.jobCount.Add(-1)
			removed++
		}
		return true
	})
	return removed
}

// KillAll terminates all background jobs.
func (m *Manager) KillAll() {
	m.jobs.Range(func(key, value any) bool {
		job := value.(*BackgroundJob)
		job.cancel()
		<-job.done
		m.jobs.Delete(key)
		m.jobCount.Add(-1)
		return true
	})
}

// startJob launches command as a tracked background job. The job runs under
// its own context.Background()-derived context so it survives cancellation of
// the originating request; the caller's ctx is intentionally not used here.
func (m *Manager) startJob(_ context.Context, command, workingDir, description string) (*BackgroundJob, error) {
	if m.jobCount.Load() >= MaxBackgroundJobs {
		return nil, fmt.Errorf("maximum background jobs (%d) reached", MaxBackgroundJobs)
	}

	id := fmt.Sprintf("%03X", m.idCounter.Add(1))
	jobCtx, cancel := context.WithCancel(context.Background())

	job := &BackgroundJob{
		ID:          id,
		Command:     command,
		Description: description,
		WorkingDir:  workingDir,
		StartTime:   time.Now(),
		ctx:         jobCtx,
		cancel:      cancel,
		stdout:      &syncBuffer{},
		stderr:      &syncBuffer{},
		done:        make(chan struct{}),
	}

	m.jobs.Store(id, job)
	m.jobCount.Add(1)

	go func() {
		defer close(job.done)

		cmd := exec.CommandContext(jobCtx, "sh", "-c", command)
		cmd.Dir = workingDir
		cmd.Stdout = job.stdout
		cmd.Stderr = job.stderr

		job.exitErr = cmd.Run()
		job.completedAt.Store(time.Now().Unix())
	}()

	return job, nil
}

func (m *Manager) removeJob(id string) {
	m.jobs.Delete(id)
	m.jobCount.Add(-1)
}

func (m *Manager) jobToResult(job *BackgroundJob) *Result {
	stdout, stderr, _, _ := job.GetOutput()
	return &Result{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: ExitCodeFromError(job.exitErr),
		Duration: time.Since(job.StartTime),
	}
}

// TruncateOutput truncates output preserving head and tail.
func TruncateOutput(content string) string {
	if len(content) <= MaxOutputLength {
		return content
	}
	half := MaxOutputLength / 2
	start := content[:half]
	end := content[len(content)-half:]
	truncatedLines := strings.Count(content[half:len(content)-half], "\n")
	return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, truncatedLines, end)
}

// ExitCodeFromError extracts the exit code from an exec error.
func ExitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

// syncBuffer is a thread-safe bytes.Buffer.
type syncBuffer struct {
	buf bytes.Buffer
	mu  sync.RWMutex
}

func (sb *syncBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.buf.String()
}
