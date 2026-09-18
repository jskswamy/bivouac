package cmd

import (
	"os"
	"path"

	"github.com/spf13/cobra"

	"github.com/jskswamy/bivouac/internal/lifecycle"
)

// syncLocalDir returns the local directory sync should push: the
// --dir flag's value if set, else the current working directory.
func syncLocalDir(dirFlag string) (string, error) {
	if dirFlag != "" {
		return dirFlag, nil
	}
	return os.Getwd()
}

// syncRemoteDir returns the remote directory sync should target:
// args[0] if given, else local's path mirrored under remoteUser's
// home via lifecycle.RemotePath (always a mirror -- under the home
// path directly if local is under the local user's home, or under
// the full absolute local path otherwise).
func syncRemoteDir(args []string, local, remoteUser string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	return lifecycle.RemotePath(local, remoteUser)
}

func runSync(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	dirFlag, err := cmd.Flags().GetString("dir")
	if err != nil {
		return err
	}
	local, err := syncLocalDir(dirFlag)
	if err != nil {
		return err
	}
	remote, err := syncRemoteDir(args, local, record.User)
	if err != nil {
		return err
	}

	if err := lifecycle.Push(cmd.Context(), record.IP, record.User, local, remote); err != nil {
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

	if err := lifecycle.Pull(cmd.Context(), record.IP, record.User, remote, local); err != nil {
		return err
	}
	cmd.Printf("Downloaded %s:%s to %s\n", name, remote, local)
	return nil
}
