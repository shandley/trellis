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
	cmd.AddCommand(channelCreateCmd())
	return cmd
}

func channelCreateCmd() *cobra.Command {
	var dm bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			kind := "channel"
			if dm {
				kind = "dm"
			}
			ch, err := c.CreateChannel(cmd.Context(), args[0], kind)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created #%s [%s]\n", ch.Name, ch.Kind)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dm, "dm", false, "create a DM-style channel instead of an open channel")
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
			if _, err := c.CreatePost(cmd.Context(), channel, body); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "posted to #%s: %q\n", channel, firstLine(body))
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
			if _, err := c.Reply(cmd.Context(), parentID, body); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "replied to %s\n", parentID)
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
			names := c.NameMap(cmd.Context())
			now := time.Now()
			for _, p := range posts {
				body := firstLine(p.Body)
				if p.Muted {
					body = "(muted) " + body
				}
				fmt.Fprintf(out, "%-4d @%s  %s  %d %s  %s\n",
					p.Seq,
					authorName(names, p.AuthorID),
					relTime(p.LastActivity, now),
					p.ReplyCount,
					plural(p.ReplyCount, "reply", "replies"),
					body,
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
		Use:   "read <post-id|channel>",
		Short: "Read a post and its subtree (folded by default; --all expands)",
		Long: "Read a post and its replies. The argument is either a post id (a short\n" +
			"prefix from `feed` is fine) or a channel name, in which case the channel's\n" +
			"most recently active post is shown.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("specify a post id or a channel name (e.g. `trellis read general`); run `trellis feed <channel>` to list posts")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			target := args[0]

			// If the argument names a channel, read that channel's most-active post.
			postID := target
			if chs, err := c.ListChannels(cmd.Context()); err == nil {
				for _, ch := range chs {
					if ch.Name == target {
						posts, ferr := c.Feed(cmd.Context(), target, 1, false, false)
						if ferr != nil {
							return ferr
						}
						if len(posts) == 0 {
							fmt.Fprintf(cmd.OutOrStdout(), "no posts in #%s\n", target)
							return nil
						}
						postID = posts[0].ID
						fmt.Fprintf(cmd.ErrOrStderr(), "most recent post in #%s:\n", target)
						break
					}
				}
			}

			pv, err := c.GetPost(cmd.Context(), postID)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(pv)
			}
			names := c.NameMap(cmd.Context())
			kids := childrenMap(pv.Nodes)
			addr := nodeAddresses(kids, pv.Post.Node, pv.Post.Seq)
			var b strings.Builder
			renderTree(&b, kids, pv.Post.Node, names, addr, all)
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
			names := c.NameMap(ctx)
			fmt.Fprintln(cmd.ErrOrStderr(), "watching… (Ctrl-C to stop)")
			for ev := range events {
				if ev.Node == nil {
					continue
				}
				n := ev.Node
				fmt.Fprintf(out, "%s  @%s: %s\n", shortID(n.ID), authorName(names, n.AuthorID), firstLine(n.Body))
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
