package cmd

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func defaultBuildInfo() BuildInfo {
	return BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}
}

func newRootCmd() *cobra.Command {
	return newRootCmdWithBuildInfo(defaultBuildInfo())
}

func newRootCmdWithBuildInfo(info BuildInfo) *cobra.Command {
	root := &cobra.Command{
		Use:   "bivouac",
		Short: "Disposable remote workspaces for coding agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.Version = info.Version
	root.SetVersionTemplate("bivouac " + info.Version + " (commit: " + info.Commit + ", built: " + info.Date + ")\n")
	if runningUnderAgent() {
		root.Long = agentJSONHint()
	}
	root.PersistentFlags().String("repo", "", "path to the repo to resolve identity from (overrides cwd-based resolution)")
	root.PersistentFlags().String("name", "", "instance name override")
	root.SilenceUsage = true

	root.AddCommand(newInitCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newUpCmd())
	root.AddCommand(newPresetCmd())
	root.AddCommand(newProvisionCmd())
	root.AddCommand(newProxyCmd())
	root.AddCommand(newSecretsCmd())
	root.AddCommand(newImageCmd())
	root.AddCommand(newManifestCmd())
	root.AddCommand(newLookupCommands()...)

	return root
}

// Execute builds a fresh command tree and runs it. Called once from main.
//
// The context is cancelled on SIGINT/SIGTERM so that every child process
// started with exec.CommandContext is reaped when bivouac goes down.
// Without it those children outlive their parent: `connect`'s ssh forward
// was observed still holding a local port after bivouac itself had exited,
// which then makes the next connect on that port fail for a reason nothing
// on screen explains. Ctrl-C in a terminal already signals the whole
// foreground process group and so appeared to work -- this covers every
// other way the process can be told to stop.
func Execute(info BuildInfo) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return newRootCmdWithBuildInfo(info).ExecuteContext(ctx)
}
