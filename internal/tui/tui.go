// Package tui is a full-screen, keyboard-driven terminal client for Trellis.
//
// It is just another client over the existing HTTP/SSE API (internal/client):
// it never touches the server, store, mcp, core, or api packages directly
// beyond reading their types. The whole experience — browsing the activity
// feed, drilling into a post's reply tree, composing posts and replies, and
// live-updating from the SSE stream — is driven entirely by the keyboard, so a
// human never has to type a post id.
//
// The implementation is a single Bubble Tea (v2) model with three modes (feed,
// thread, compose). All network work happens in tea.Cmds that return result
// messages; Update never blocks. See model.go for the model, update.go for the
// message/key handling, view.go for rendering, and cmds.go for the commands.
package tui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/shandley/trellis/internal/client"
	"github.com/spf13/cobra"
)

// Command returns the `trellis tui` cobra command. It builds a client (failing
// clearly if not logged in), runs the Bubble Tea program, and translates the
// "no TTY" failure into a friendly error instead of a panic/hang.
func Command() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive terminal UI",
		Long: "Open a full-screen, keyboard-driven client over the Trellis API.\n" +
			"Browse the feed, drill into threads, compose posts and replies, and\n" +
			"watch the conversation update live — without ever typing a post id.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client.DefaultClient()
			if err != nil {
				return fmt.Errorf("not logged in: run `trellis login --server <url> --token <token>` first (%w)", err)
			}

			m := newModel(c)
			p := tea.NewProgram(m, tea.WithContext(cmd.Context()))
			if _, err := p.Run(); err != nil {
				// Bubble Tea fails to open the input/output when there is no
				// controlling terminal (e.g. piped, CI). Surface that clearly
				// rather than dumping a raw error.
				if isNoTTY(err) {
					return errors.New("trellis tui requires an interactive terminal")
				}
				return err
			}
			return nil
		},
	}
}

// isNoTTY reports whether err looks like a "not a terminal" failure from the
// Bubble Tea program startup. We match on substrings because the exact error
// type is internal to the input/term layers.
func isNoTTY(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"not a tty", "not a terminal", "/dev/tty", "inappropriate ioctl", "device not configured", "open /dev"} {
		if containsFold(msg, s) {
			return true
		}
	}
	return false
}

// containsFold is a tiny case-insensitive substring check (avoids importing
// strings just for this in the command file; the rest of the package uses
// strings freely).
func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// ctxOrBackground returns the model's context, defaulting to a background
// context so cmds never get a nil context.
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
