package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
)

// RemotePath computes where localPath lands on the instance as user:
// if localPath is under the local user's home directory, it's mirrored
// under the remote user's home (e.g. local
// /Users/subramk/source/cloudlab with remote user "subramk" becomes
// /home/subramk/source/cloudlab). A localPath outside the local home is
// mirrored too, at /home/<remoteUser><localPath> (e.g. /opt/work/repo
// becomes /home/subramk/opt/work/repo) -- every repo location gets a
// predictable, always-writable remote destination under the remote
// user's own home, not just ones under the local $HOME (an as-is
// passthrough would often point at a path the unprivileged remote user
// can't create, failing `up` only after the instance is already
// provisioned). cloud-init.sh creates the remote user via a plain
// `useradd --create-home` with no --home-dir override, so its home is
// always exactly /home/<user> -- the standard Debian/Ubuntu default.
func RemotePath(localPath, remoteUser string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	local, err := filepath.Abs(localPath)
	if err != nil {
		return "", err
	}
	home = filepath.Clean(home)

	if local == home {
		return "/home/" + remoteUser, nil
	}
	if rel, ok := strings.CutPrefix(local, home+string(filepath.Separator)); ok {
		return "/home/" + remoteUser + "/" + rel, nil
	}
	return "/home/" + remoteUser + local, nil
}
