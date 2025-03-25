package script

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/statico/llmscript/internal/llm"
)

// Executor handles running scripts in a controlled environment
type Executor struct {
	workDir string
	shell   *ShellConfig
}

// NewExecutor creates a new script executor
func NewExecutor(workDir string) (*Executor, error) {
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create work directory: %w", err)
	}

	shell, err := DetectShell()
	if err != nil {
		return nil, fmt.Errorf("failed to detect shell: %w", err)
	}

	return &Executor{
		workDir: workDir,
		shell:   shell,
	}, nil
}

// ExecuteTest runs a single test case in a controlled environment
func (e *Executor) ExecuteTest(ctx context.Context, script string, test llm.Test) (string, error) {
	// Validate script for security
	if err := ValidateScript(script); err != nil {
		return "", fmt.Errorf("script validation failed: %w", err)
	}

	// Create a secure temporary directory for this test
	testDir, err := PrepareScriptEnvironment(e.workDir)
	if err != nil {
		return "", fmt.Errorf("failed to prepare test environment: %w", err)
	}
	defer os.RemoveAll(testDir)

	// Write the script to a file
	scriptPath := filepath.Join(testDir, "script.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0750); err != nil {
		return "", fmt.Errorf("failed to write script: %w", err)
	}

	// Run setup commands
	for _, cmd := range test.Setup {
		if err := e.runCommand(ctx, testDir, cmd, test.Environment); err != nil {
			return "", fmt.Errorf("setup command failed: %w", err)
		}
	}

	// Run the script with the test input
	args := append(e.shell.Args, scriptPath)
	cmd := exec.CommandContext(ctx, e.shell.Path, args...)
	cmd.Dir = testDir
	cmd.Env = e.buildEnv(test.Environment)

	// Set up input/output pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	defer stdin.Close()

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Start the command
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start command: %w", err)
	}

	// Write input and close stdin
	if _, err := stdin.Write([]byte(test.Input)); err != nil {
		return "", fmt.Errorf("failed to write input: %w", err)
	}
	if err := stdin.Close(); err != nil {
		return "", fmt.Errorf("failed to close stdin: %w", err)
	}

	// Wait for completion with timeout
	done := make(chan error)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
		}
	case <-ctx.Done():
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return "", fmt.Errorf("command timed out")
	}

	return stdout.String(), nil
}

// runCommand executes a shell command in the given directory
func (e *Executor) runCommand(ctx context.Context, dir, cmd string, env map[string]string) error {
	args := append(e.shell.Args, cmd)
	command := exec.CommandContext(ctx, e.shell.Path, args...)
	command.Dir = dir
	command.Env = e.buildEnv(env)

	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %w\noutput: %s", err, output)
	}
	return nil
}

// buildEnv builds the environment variables for a command
func (e *Executor) buildEnv(env map[string]string) []string {
	// Start with current environment
	result := os.Environ()

	// Add test-specific environment variables
	for k, v := range env {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}

	return result
}
