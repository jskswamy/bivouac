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
// /home/subramk/source/cloudlab); otherwise localPath is used as-is,
// the identical absolute path on both ends. cloud-init.sh creates the
// remote user via a plain `useradd --create-home` with no --home-dir
// override, so its home is always exactly /home/<user> -- the standard
// Debian/Ubuntu default.
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
	return local, nil
}
