package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	defaultRuntimeStdout = 2 << 20
	defaultRuntimeStderr = 64 << 10
)

type processResult struct {
	stdout    []byte
	stderr    []byte
	exitCode  int
	truncated bool
	err       error
}

// runProcess executes one local command with a bounded timeout context, bounded
// output, and an explicit environment. It never invokes a shell.
func runProcess(ctx context.Context, command string, args []string, stdin []byte, dir string, env []string, maxStdout, maxStderr int) processResult {
	if maxStdout <= 0 {
		maxStdout = defaultRuntimeStdout
	}
	if maxStderr <= 0 {
		maxStderr = defaultRuntimeStderr
	}
	cmd := newRuntimeCommand(ctx, command, args, stdin, dir, env)
	stdout := newLimitedBuffer(maxStdout)
	stderr := newLimitedBuffer(maxStderr)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return processResult{err: runtimeStartError(err)}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-done:
		}
	}()
	err := cmd.Wait()
	once.Do(func() { close(done) })
	result := finishProcess(ctx, cmd, stdout, stderr, err)
	if result.err == nil && stdout.Truncated() {
		result.err = fmt.Errorf("runtime output exceeded %d bytes", maxStdout)
	}
	return result
}

// runProcessStreaming is runProcess with a line callback. Stdout is still
// bounded and retained so a runtime that ignores the streaming protocol can
// fall back to its complete response. onLine is called synchronously for each
// line before the process is reaped; the slice is only valid during the call.
func runProcessStreaming(ctx context.Context, command string, args []string, stdin []byte, dir string, env []string, maxStdout, maxStderr int, onLine func([]byte)) processResult {
	if maxStdout <= 0 {
		maxStdout = defaultRuntimeStdout
	}
	if maxStderr <= 0 {
		maxStderr = defaultRuntimeStderr
	}
	cmd := newRuntimeCommand(ctx, command, args, stdin, dir, env)
	stdout := newLimitedBuffer(maxStdout)
	stderr := newLimitedBuffer(maxStderr)
	cmd.Stderr = stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return processResult{err: fmt.Errorf("open runtime stdout: %w", err)}
	}

	if err := cmd.Start(); err != nil {
		_ = pipe.Close()
		return processResult{err: runtimeStartError(err)}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-done:
		}
	}()

	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 64*1024), defaultRuntimeStdout)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		_, _ = stdout.Write(line)
		_, _ = stdout.Write([]byte("\n"))
		if onLine != nil {
			onLine(line)
		}
	}
	readErr := scanner.Err()
	if readErr != nil {
		// A scanner failure means nobody will drain stdout. Stop the runtime
		// before waiting so it cannot block forever on a full pipe.
		killProcessGroup(cmd)
	}
	waitErr := cmd.Wait()
	once.Do(func() { close(done) })

	result := finishProcess(ctx, cmd, stdout, stderr, waitErr)
	if readErr != nil && ctx.Err() == nil && result.err == nil {
		result.err = fmt.Errorf("read runtime output: %w", readErr)
	}
	return result
}

func newRuntimeCommand(ctx context.Context, command string, args []string, stdin []byte, dir string, env []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(os.Environ(), env)
	cmd.Stdin = bytes.NewReader(stdin)
	configureProcess(cmd)
	return cmd
}

func finishProcess(ctx context.Context, cmd *exec.Cmd, stdout, stderr *limitedBuffer, waitErr error) processResult {
	result := processResult{
		stdout:    stdout.Bytes(),
		stderr:    stderr.Bytes(),
		truncated: stdout.Truncated() || stderr.Truncated(),
	}
	if cmd.ProcessState != nil {
		result.exitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.err = ErrRuntimeTimeout
		return result
	}
	if ctx.Err() == context.Canceled {
		result.err = context.Canceled
		return result
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			message := strings.TrimSpace(string(result.stderr))
			if message == "" {
				message = strings.TrimSpace(string(result.stdout))
			}
			message = truncate(message, 1000)
			if message != "" {
				result.err = fmt.Errorf("runtime exited with code %d: %s", result.exitCode, message)
			} else {
				result.err = fmt.Errorf("runtime exited with code %d", result.exitCode)
			}
			return result
		}
		result.err = runtimeStartError(waitErr)
		return result
	}
	// Truncation is not always fatal: a streaming caller may have already
	// received the final result. The caller decides whether the retained raw
	// output is still needed.
	return result
}

func runtimeStartError(err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	return fmt.Errorf("start runtime: %w", err)
}

func mergeEnv(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	keys := make(map[string]bool, len(overrides))
	for _, item := range overrides {
		if key, _, ok := strings.Cut(item, "="); ok {
			keys[key] = true
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, ok := strings.Cut(item, "=")
		if ok && keys[key] {
			continue
		}
		out = append(out, item)
	}
	return append(out, overrides...)
}

// limitedBuffer stores at most limit bytes, discarding the rest while noting
// truncation. It is safe for the os/exec copy goroutines.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(p)
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		_, _ = b.buf.Write(p)
	} else if original > 0 {
		b.truncated = true
	}
	return original, nil
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

func truncate(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	// Prefer a rune boundary so the room never receives invalid UTF-8.
	cut := limit
	for cut > 0 && (text[cut]&0xc0) == 0x80 {
		cut--
	}
	return text[:cut]
}

// waitForProcessExit is used by tests and runtimes that want to bound a child
// independently of the parent context.
func waitForProcessExit(ctx context.Context, done <-chan error, grace time.Duration) error {
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		select {
		case err := <-done:
			return err
		case <-time.After(grace):
			return ctx.Err()
		}
	}
}

var _ io.Writer = (*limitedBuffer)(nil)
