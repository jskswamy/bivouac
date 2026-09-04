package cmd

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jskswamy/cloudlab/internal/identity"
	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provider/digitalocean"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// resolveInstance opens the state store, looks up name, and returns
// the same "no instance named" error every command below list/up
// reports identically when the name doesn't resolve to a known
// instance.
func resolveInstance(name string) (*state.Store, state.Record, error) {
	store, err := state.Open()
	if err != nil {
		return nil, state.Record{}, err
	}
	record, ok, err := store.Get(name)
	if err != nil {
		return nil, state.Record{}, err
	}
	if !ok {
		return nil, state.Record{}, fmt.Errorf("no instance named %q (run cloudlab up first)", name)
	}
	return store, record, nil
}

// resolveProvider builds a DigitalOcean provider from
// DIGITALOCEAN_TOKEN, for commands that need to call the live API
// (down, status).
func resolveProvider() (provider.Provider, error) {
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("DIGITALOCEAN_TOKEN not set")
	}
	return digitalocean.New(token), nil
}

func runDown(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	p, err := resolveProvider()
	if err != nil {
		return err
	}

	ok, err := confirm(cmd, downSummary(record))
	if err != nil {
		return err
	}
	if !ok {
		cmd.Println("Aborted.")
		return nil
	}

	if err := lifecycle.Down(cmd.Context(), p, store, record); err != nil {
		return err
	}
	cmd.Printf("Instance %s is down\n", name)
	return nil
}

// downSummary describes the instance down is about to destroy, and
// warns the destruction is unrecoverable, for confirmation before
// anything irreversible happens.
func downSummary(record state.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This will destroy instance %q -- this cannot be undone:\n", record.Name)
	fmt.Fprintf(&b, "  Provider: %s\n", record.Provider)
	fmt.Fprintf(&b, "  Region:   %s\n", record.Region)
	fmt.Fprintf(&b, "  Size:     %s\n", record.Size)
	fmt.Fprintf(&b, "  Template: %s\n", record.Template)
	fmt.Fprintf(&b, "  IP:       %s\n", record.IP)
	b.WriteString("Any unsaved work on the instance will be lost.\n")
	b.WriteString("Proceed? [y/N]: ")
	return b.String()
}

func runStatus(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	p, err := resolveProvider()
	if err != nil {
		return err
	}

	st := lifecycle.Status(cmd.Context(), p, record)
	cmd.Printf("Name:     %s\n", st.Record.Name)
	cmd.Printf("Provider: %s\n", st.Record.Provider)
	cmd.Printf("Region:   %s\n", st.Record.Region)
	cmd.Printf("Size:     %s\n", st.Record.Size)
	cmd.Printf("Template: %s\n", st.Record.Template)
	cmd.Printf("IP:       %s\n", st.Record.IP)
	if st.LiveErr != nil {
		cmd.Printf("Status:   unknown (live check failed: %v)\n", st.LiveErr)
	} else {
		cmd.Printf("Status:   %s\n", st.LiveStatus)
	}
	return nil
}

func runWatch(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoFlag, _ := cmd.Flags().GetString("repo")
	root, err := identity.RepoRoot(cwd, repoFlag)
	if err != nil {
		return err
	}

	if err := lifecycle.StartWatch(cmd.Context(), record.IP, name, root); err != nil {
		return err
	}
	cmd.Printf("Watch restarted for %s\n", name)
	return nil
}

// defaultRemoteDir returns the instance-side directory sync uses when
// no remote-dir is given: ~/<basename(local)>.
func defaultRemoteDir(local string) string {
	return "~/" + filepath.Base(filepath.Clean(local))
}

func runSync(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	local := args[0]
	remote := defaultRemoteDir(local)
	if len(args) > 1 {
		remote = args[1]
	}

	if err := lifecycle.Push(cmd.Context(), record.IP, local, remote); err != nil {
		return err
	}
	cmd.Printf("Synced %s to %s:%s\n", local, name, remote)
	return nil
}

// defaultLocalDir returns the local directory download uses when no
// local-dir is given: ./<basename(remote)>. remote is always a POSIX
// path (the instance is always Linux), so path.Base is used rather
// than filepath.Base.
func defaultLocalDir(remote string) string {
	return "./" + path.Base(path.Clean(remote))
}

func runDownload(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	remote := args[0]
	local := defaultLocalDir(remote)
	if len(args) > 1 {
		local = args[1]
	}

	if err := lifecycle.Pull(cmd.Context(), record.IP, remote, local); err != nil {
		return err
	}
	cmd.Printf("Downloaded %s:%s to %s\n", name, remote, local)
	return nil
}

func runSSH(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	return lifecycle.SSH(cmd.Context(), record.IP)
}
