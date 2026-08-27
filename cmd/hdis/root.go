package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/husniadil/herdr-dispatch/internal/cli"
	"github.com/husniadil/herdr-dispatch/internal/config"
	"github.com/husniadil/herdr-dispatch/internal/mcpdoor"
	"github.com/husniadil/herdr-dispatch/internal/spawn"
	"github.com/husniadil/herdr-dispatch/internal/version"
)

// newRootCmd is the whole command tree: the verbs come from the registry both
// doors are generated from, and the three commands that are not verbs — the
// daemon, the MCP door and the version — are added here beside them.
func newRootCmd() *cobra.Command {
	root := cli.Root(cli.Send)
	root.AddCommand(newDaemonCmd(), newMCPCmd(), newVersionCmd())
	return root
}

// daemonFlags are the dispatcher's own knobs. They stay flags on this
// subcommand rather than globals: they configure the process that owns the
// tick, and mean nothing to a client call.
type daemonFlags struct {
	configPath     string
	logPath        string
	interval       time.Duration
	once           bool
	basePane       string
	maxWorkers     int
	claimTimeout   time.Duration
	maxPrompts     int
	startTimeout   time.Duration
	confirmCeiling time.Duration
}

func newDaemonCmd() *cobra.Command {
	f := &daemonFlags{}
	cmd := &cobra.Command{
		Use:     "daemon",
		Aliases: []string{"run"},
		Short:   "Own the tick and answer both doors",
		Long: "The daemon owns the tick and the bindings, and answers the socket both " +
			"doors dial. `hdis run` is the same command.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return serve(f) },
	}
	fs := cmd.Flags()
	fs.StringVar(&f.configPath, "config", config.ConfigPath(), "worker profiles and per-project overrides")
	fs.StringVar(&f.logPath, "log", config.LogPath(), "the file the log is appended to; stdout keeps every line too")
	fs.DurationVar(&f.interval, "interval", 15*time.Second, "how often to tick")
	fs.BoolVar(&f.once, "once", false, "run one tick and exit")
	fs.StringVar(&f.basePane, "pane", os.Getenv("HERDR_PANE_ID"), "the pane worker panes are split off")
	fs.IntVar(&f.maxWorkers, "max-workers", 0, `how many workers may be live at once; 0 means the config's "max_workers"`)
	fs.DurationVar(&f.claimTimeout, "claim-timeout", 5*time.Minute, "how long a delivered goal may go unclaimed before a nudge")
	fs.IntVar(&f.maxPrompts, "max-prompts", 3, "how many times one task's goal may be delivered before giving up")
	fs.DurationVar(&f.startTimeout, "start-timeout", 45*time.Second, "how long herdr waits for a worker to become interactive")
	fs.DurationVar(&f.confirmCeiling, "confirm-ceiling", spawn.DefaultConfirmCeiling, "how long to wait for a delivered goal to show on the worker's screen")
	return cmd
}

func newMCPCmd() *cobra.Command {
	var opt mcpdoor.Options
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the same verbs over stdio MCP",
		Long: "A thin door over the same daemon calls the CLI makes (§7.2). Both surfaces\n" +
			"are first-class and the door serves every verb the CLI serves (§7.3). What a\n" +
			"door cannot have is `--as`, which is an identity claim carried BY a call;\n" +
			"--operator is its counterpart and travels with this process instead (§7.5).",
		// The door takes no positional arguments, and silently ignoring one
		// is how a caller ends up believing it passed something that took
		// effect.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcpdoor.Serve(context.Background(), version.Version, nil, opt)
		},
	}
	// §7.5: read once, from the server command. It is deliberately NOT a
	// persistent flag — a flag every verb carried would be a per-call
	// declaration, which is the thing this exists instead of.
	cmd.Flags().BoolVar(&opt.Operator, "operator", false,
		"Declare that this door speaks for the operator (§7.5). Set it once, in the client's\n"+
			"server configuration, where a human wrote it deliberately. Without it a door in no\n"+
			"Herdr pane is nobody, because absence of evidence is not evidence of the operator.")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the plugin version and the contract it satisfies",
		// The contract revision travels with the version, the way both
		// siblings print it: §13.4 makes a plugin declare it, and a reader
		// comparing three binaries should not have to run a different verb
		// on one of them to see it. `--json` answers the same three facts.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if on, _ := cmd.Flags().GetBool("json"); on {
				out, _ := json.Marshal(map[string]string{
					"version": version.Version, "contract": version.Contract, "plugin": "herdr-dispatch",
				})
				fmt.Println(string(out))
				return nil
			}
			fmt.Printf("hdis %s (herdr-dispatch), shared plugin contract %s\n", version.Version, version.Contract)
			return nil
		},
	}
}
