package client

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Commands returns all client subcommands for main.go to register on the root.
func Commands() []*cobra.Command {
	return []*cobra.Command{
		loginCmd(),
		whoamiCmd(),
		adduserCmd(),
		channelsCmd(),
		postCmd(),
		replyCmd(),
		feedCmd(),
		readCmd(),
		watchCmd(),
		muteCmd(),
	}
}

// newClient builds a Client from resolved config. Commands that need a token
// surface a clear error when none is configured.
func newClient() (*Client, error) {
	server, token := loadConfig()
	if token == "" {
		return nil, fmt.Errorf("no token configured; run `trellis login --server <url> --token <token>` or set TRELLIS_TOKEN")
	}
	return NewClient(server, token), nil
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// bodyFromArgsOrStdin joins args into a body, or reads stdin when args are
// empty (pipe-friendly). The result is trimmed of surrounding whitespace.
func bodyFromArgsOrStdin(in io.Reader, args []string) (string, error) {
	if len(args) > 0 {
		return strings.TrimSpace(strings.Join(args, " ")), nil
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func loginCmd() *cobra.Command {
	var server, token string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save server URL and token to the config file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				return fmt.Errorf("--token is required")
			}
			cfg := Config{Server: server, Token: token}
			path, err := writeConfigFile(cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "logged in to %s\nconfig written to %s\n", server, path)
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", defaultServer(), "Trellis server URL")
	cmd.Flags().StringVar(&token, "token", "", "bearer token (required)")
	return cmd
}

// defaultServer is the server value pre-filled for `login`, honoring env/file.
func defaultServer() string {
	s, _ := loadConfig()
	return s
}

func whoamiCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the authenticated participant",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			p, err := c.Whoami(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(p)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "@%s (%s) — %s [%s]\n", p.Handle, p.DisplayName, p.ID, p.Kind)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print raw JSON")
	return cmd
}

func adduserCmd() *cobra.Command {
	var name, kind string
	cmd := &cobra.Command{
		Use:   "adduser <handle>",
		Short: "Create a participant (human or agent); prints its token once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			p, token, err := c.CreateParticipant(cmd.Context(), args[0], name, kind)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "created @%s [%s]\n", p.Handle, p.Kind)
			fmt.Fprintf(out, "token (shown once): %s\n", token)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "display name")
	cmd.Flags().StringVar(&kind, "kind", "human", "participant kind: human|agent")
	return cmd
}

func channelsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "List channels",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			chs, err := c.ListChannels(cmd.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(chs)
			}
			out := cmd.OutOrStdout()
			if len(chs) == 0 {
				fmt.Fprintln(out, "no channels")
				return nil
			}
			for _, ch := range chs {
				fmt.Fprintf(out, "#%s [%s]\n", ch.Name, ch.Kind)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print raw JSON")
	return cmd
}

func postCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "post <channel> [body...]",
		Short: "Create a post in a channel (body from args or STDIN)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			channel := args[0]
			body, err := bodyFromArgsOrStdin(cmd.InOrStdin(), args[1:])
			if err != nil {
				return err
			}
			if body == "" {
				return fmt.Errorf("empty body")
			}
			n, err := c.CreatePost(cmd.Context(), channel, body)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "posted %s\n", n.ID)
			return nil
		},
	}
	return cmd
}

func replyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reply <node-id> [body...]",
		Short: "Reply to any node (post or comment); body from args or STDIN",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			parentID := args[0]
			body, err := bodyFromArgsOrStdin(cmd.InOrStdin(), args[1:])
			if err != nil {
				return err
			}
			if body == "" {
				return fmt.Errorf("empty body")
			}
			n, err := c.Reply(cmd.Context(), parentID, body)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "replied %s\n", n.ID)
			return nil
		},
	}
	return cmd
}

func feedCmd() *cobra.Command {
	var (
		limit    int
		unread   bool
		mentions bool
		asJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "feed <channel>",
		Short: "Show a channel's posts, newest activity first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			posts, err := c.Feed(cmd.Context(), args[0], limit, unread, mentions)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(posts)
			}
			out := cmd.OutOrStdout()
			if len(posts) == 0 {
				fmt.Fprintln(out, "no posts")
				return nil
			}
			now := time.Now()
			for _, p := range posts {
				fmt.Fprintf(out, "%s  @%s  %s  %d %s  %s\n",
					shortID(p.ID),
					p.AuthorID,
					relTime(p.LastActivity, now),
					p.ReplyCount,
					plural(p.ReplyCount, "reply", "replies"),
					firstLine(p.Body),
				)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "max posts (server default if 0)")
	cmd.Flags().BoolVar(&unread, "unread", false, "only posts with activity after your watermark")
	cmd.Flags().BoolVar(&mentions, "mentions", false, "only posts whose subtree mentions you")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print raw JSON")
	return cmd
}

func readCmd() *cobra.Command {
	var (
		all    bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "read <post-id>",
		Short: "Read a post and its subtree (folded by default; --all expands)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			pv, err := c.GetPost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(pv)
			}
			kids := childrenMap(pv.Nodes)
			var b strings.Builder
			renderTree(&b, kids, pv.Post.Node, all)
			fmt.Fprint(cmd.OutOrStdout(), b.String())
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "expand the entire tree")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print raw JSON")
	return cmd
}

func watchCmd() *cobra.Command {
	var mentions bool
	cmd := &cobra.Command{
		Use:   "watch [channel]",
		Short: "Stream new nodes live (tail -f style) until Ctrl-C",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var channels []string
			if len(args) == 1 {
				channels = []string{args[0]}
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			events, err := c.Events(ctx, channels, mentions)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(cmd.ErrOrStderr(), "watching… (Ctrl-C to stop)")
			for ev := range events {
				if ev.Node == nil {
					continue
				}
				n := ev.Node
				fmt.Fprintf(out, "%s  @%s: %s\n", shortID(n.ID), n.AuthorID, firstLine(n.Body))
			}
			return ctx.Err()
		},
	}
	cmd.Flags().BoolVar(&mentions, "mentions", false, "only events that mention you")
	return cmd
}

func muteCmd() *cobra.Command {
	var unmute bool
	cmd := &cobra.Command{
		Use:   "mute <post-id>",
		Short: "Mute (or --unmute) a post so it stops resurfacing for you",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			if err := c.Mute(cmd.Context(), args[0], !unmute); err != nil {
				return err
			}
			action := "muted"
			if unmute {
				action = "unmuted"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", action, args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&unmute, "unmute", false, "unmute instead of mute")
	return cmd
}
