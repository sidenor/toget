package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sidenor/toget/internal/agent"
	"github.com/sidenor/toget/sdk/connector"
)

type connectorCheckSummary struct {
	OK              bool     `json:"ok"`
	Version         int      `json:"version"`
	Command         string   `json:"command"`
	Args            []string `json:"args,omitempty"`
	DurationMS      int64    `json:"duration_ms"`
	Deltas          int      `json:"deltas"`
	Resets          int      `json:"resets"`
	DeltaBytes      int      `json:"delta_bytes"`
	Text            string   `json:"text,omitempty"`
	Format          string   `json:"format,omitempty"`
	SessionID       string   `json:"session_id,omitempty"`
	WaitingApproval bool     `json:"waiting_approval,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func newConnectorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connector",
		Short: "Develop and test Agent connectors",
	}
	cmd.AddCommand(newConnectorCheckCommand())
	return cmd
}

func newConnectorCheckCommand() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "check [--timeout 30s] -- <command> [args...]",
		Short: "Run a connector against a synthetic v1 request",
		Long: "Run a connector through the same command runtime path as the daemon,\n" +
			"using a synthetic connector protocol version 1 request. The command runs in\n" +
			"a fresh workspace and does not need a running node or a registered Agent.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("connector command is required")
			}
			if timeout <= 0 {
				return errors.New("timeout must be positive")
			}
			summary, err := runConnectorCheck(cmd.Context(), args[0], args[1:], timeout)
			summary.OK = err == nil
			if err != nil {
				err = friendlyConnectorCheckError(err, timeout)
				summary.Error = err.Error()
			}
			if jsonFlag {
				if printErr := printJSON(summary); printErr != nil {
					return printErr
				}
				if err != nil {
					return err
				}
				return nil
			}
			if err != nil {
				return summarizeConnectorCheckError(summary, err, timeout)
			}
			printConnectorCheckSummary(summary)
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "maximum time for one invocation")
	// Everything after the connector path belongs to the connector, including
	// flags such as --json or --timeout.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// runConnectorCheck invokes the connector through the normal command runtime,
// so the request, environment, working directory, limits, and process-group
// termination are the same as during a real Agent run.
func runConnectorCheck(ctx context.Context, command string, args []string, timeout time.Duration) (connectorCheckSummary, error) {
	started := time.Now()
	summary := connectorCheckSummary{
		Version: connector.Version,
		Command: command,
		Args:    append([]string(nil), args...),
	}

	root, err := os.MkdirTemp("", "toget-connector-check-")
	if err != nil {
		return summary, fmt.Errorf("create check workspace: %w", err)
	}
	defer os.RemoveAll(root)
	home := filepath.Join(root, "home")
	workdir := filepath.Join(root, "agents", "connector-check")
	for _, dir := range []string{home, workdir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return summary, fmt.Errorf("create check workspace: %w", err)
		}
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	req := agent.RunRequest{
		Version: connector.Version,
		Agent: agent.Agent{
			ID: "connector-check-agent", AccountID: "connector-check-account",
			Name: "Connector Check", Runtime: "connector-check", Status: agent.StatusIdle,
			Permissions: json.RawMessage(`{}`), CreatedAt: now,
		},
		Room: agent.RunRoom{ID: "connector-check-room", Name: "Connector Check"},
		Trigger: agent.Trigger{
			EventID: "connector-check-event", Seq: 1,
			AuthorID: "connector-check-account", AuthorName: "Connector Check",
			Text:   "Hello from toget connector check. Reply briefly to verify the connector.",
			Format: "plain", CreatedAt: now,
		},
		History: []agent.HistoryEntry{{
			Seq: 1, EventID: "connector-check-event", Type: "message.posted",
			AuthorID: "connector-check-account", AuthorName: "Connector Check",
			Text:      "Hello from toget connector check. Reply briefly to verify the connector.",
			CreatedAt: now,
		}},
		Home:    home,
		Workdir: workdir,
	}

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var streamed strings.Builder
	runtime := agent.NewCommandRuntime("connector-check", agent.CommandOptions{
		Command: command, Args: args, Workdir: workdir, Env: runtimeSubprocessEnv(),
	})
	result, runErr := runtime.RunStream(checkCtx, req, func(delta agent.StreamDelta) {
		if delta.Reset {
			summary.Resets++
			streamed.Reset()
			return
		}
		if delta.Text == "" {
			return
		}
		summary.Deltas++
		streamed.WriteString(delta.Text)
	})
	summary.DurationMS = time.Since(started).Milliseconds()
	summary.DeltaBytes = streamed.Len()
	summary.Text = result.Text
	summary.Format = result.Format
	summary.SessionID = result.SessionID
	summary.WaitingApproval = result.WaitingApproval
	return summary, runErr
}

func friendlyConnectorCheckError(err error, timeout time.Duration) error {
	switch {
	case errors.Is(err, agent.ErrRuntimeTimeout):
		return fmt.Errorf("connector timed out after %s", timeout)
	case errors.Is(err, agent.ErrRuntimeUnavailable):
		return fmt.Errorf("connector command could not be started: %w", err)
	default:
		return err
	}
}

func summarizeConnectorCheckError(summary connectorCheckSummary, err error, timeout time.Duration) error {
	err = friendlyConnectorCheckError(err, timeout)
	if summary.Deltas == 0 && summary.Resets == 0 {
		return err
	}
	return fmt.Errorf("%w (after %d delta(s), %d visible byte(s), %d reset(s))",
		err, summary.Deltas, summary.DeltaBytes, summary.Resets)
}

func printConnectorCheckSummary(summary connectorCheckSummary) {
	fmt.Printf("Connector OK in %dms\n", summary.DurationMS)
	fmt.Printf("  protocol: v%d\n", summary.Version)
	if len(summary.Args) == 0 {
		fmt.Printf("  command:  %s\n", summary.Command)
	} else {
		fmt.Printf("  command:  %s %s\n", summary.Command, strings.Join(summary.Args, " "))
	}
	fmt.Printf("  stream:   %d delta(s), %d byte(s), %d reset(s)\n",
		summary.Deltas, summary.DeltaBytes, summary.Resets)
	if summary.Text == "" {
		fmt.Println("  result:   (no reply)")
	} else {
		format := summary.Format
		if format == "" {
			format = "plain"
		}
		fmt.Printf("  result:   %s, %d byte(s)\n", format, len(summary.Text))
	}
	if summary.SessionID != "" {
		fmt.Printf("  session:  %s\n", summary.SessionID)
	}
	if summary.WaitingApproval {
		fmt.Println("  waiting:  approval")
	}
	if summary.Text != "" {
		fmt.Println("---")
		fmt.Println(summary.Text)
	}
}
