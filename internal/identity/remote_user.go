package identity

import (
	"os/user"
	"regexp"
	"strings"
)

// currentUser is a var, not a call, so tests can substitute a fake
// local user without needing to run as a specific real one.
var currentUser = user.Current

// invalidUsernameChar matches anything not valid inside a Linux
// username after the first character.
var invalidUsernameChar = regexp.MustCompile(`[^a-z0-9_-]`)

// maxUsernameLen is useradd's own default limit.
const maxUsernameLen = 32

// RemoteUser derives the instance's non-root login name from the local
// OS user (via os/user, not by shelling out to whoami), sanitized to a
// valid Linux username: any DOMAIN\ prefix stripped, lowercased,
// invalid characters replaced with "-", prefixed with "u" if it
// doesn't start with a letter, and capped at 32 characters. Falls back
// to "cloudlab" if nothing usable remains.
//
// The result is meant to be stored once (in state.Record.User) at
// instance-creation time, not re-derived on every command -- a later
// command may run as a different local user or on a different
// machine, and must keep talking to whichever user the instance was
// actually provisioned with.
func RemoteUser() (string, error) {
	u, err := currentUser()
	if err != nil {
		return "", err
	}
	return sanitizeUsername(u.Username), nil
}

func sanitizeUsername(raw string) string {
	name := strings.ToLower(raw)
	if idx := strings.LastIndexByte(name, '\\'); idx >= 0 {
		name = name[idx+1:]
	}
	name = invalidUsernameChar.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "cloudlab"
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = "u" + name
	}
	if len(name) > maxUsernameLen {
		name = name[:maxUsernameLen]
	}
	name = strings.Trim(name, "-")
	if name == "" {
		return "cloudlab"
	}
	return name
}
