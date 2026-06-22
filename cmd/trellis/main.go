// Command trellis is the single Trellis binary: a server, a CLI client, and an
// MCP server, all over the same wire protocol.
//
//	trellis serve     # run the conversation server
//	trellis post ...  # client commands (post, reply, feed, read, watch, ...)
//	trellis mcp       # expose post/reply/wait_for_mention to agents over stdio MCP
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shandley/trellis/internal/client"
	"github.com/shandley/trellis/internal/core"
	"github.com/shandley/trellis/internal/mcp"
	"github.com/shandley/trellis/internal/server"
	"github.com/shandley/trellis/internal/store"
	"github.com/shandley/trellis/internal/tui"
	"github.com/spf13/cobra"
)

// defaultDBPath returns a stable per-user location for the database
// ($XDG_DATA_HOME/trellis/trellis.db, falling back to ~/.local/share).
func defaultDBPath() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "trellis.db"
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "trellis", "trellis.db")
}

func main() {
	root := &cobra.Command{
		Use:   "trellis",
		Short: "Trellis — a CLI-first, agent-native conversation space",
		Long: "Trellis is a small conversation server with a better primitive than Slack:\n" +
			"posts you can reply to at any depth, ordered by activity so live threads resurface.\n" +
			"Humans and agents are the same kind of participant.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(serveCommand())
	root.AddCommand(mcp.Command())
	root.AddCommand(tui.Command())
	for _, c := range client.Commands() {
		root.AddCommand(c)
	}

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "trellis: "+err.Error())
		os.Exit(1)
	}
}

// serveCommand wires the SQLite store to the HTTP handler and bootstraps the
// first (owner) participant on an empty database, printing its token once.
func serveCommand() *cobra.Command {
	var (
		dbPath string
		addr   string
		owner  string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Trellis conversation server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
				return fmt.Errorf("create data dir: %w", err)
			}
			st, err := store.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer st.Close()

			ctx := context.Background()
			if n, err := st.CountParticipants(ctx); err != nil {
				return err
			} else if n == 0 {
				p, err := st.CreateParticipant(ctx, owner, owner, core.KindHuman)
				if err != nil {
					return fmt.Errorf("bootstrap owner: %w", err)
				}
				if _, err := st.CreateChannel(ctx, "general", core.ChannelOpen); err != nil {
					return fmt.Errorf("bootstrap channel: %w", err)
				}
				fmt.Fprintf(os.Stderr, "bootstrapped owner %q\n  token: %s\n  run: trellis login --server http://localhost%s --token %s\n",
					p.Handle, p.Token, addr, p.Token)
			}

			return server.Serve(ctx, addr, st)
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", defaultDBPath(), "path to the SQLite database file")
	cmd.Flags().StringVar(&addr, "addr", ":8787", "address to listen on")
	cmd.Flags().StringVar(&owner, "owner", "scott", "handle for the bootstrap owner participant")
	return cmd
}
