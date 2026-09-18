package identity

import (
	"os/user"
	"regexp"
	"strconv"
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

// validRemoteUser matches a name the instance will accept as a login: a
// letter, then up to maxUsernameLen-1 more username characters.
//
// Built from maxUsernameLen rather than written out, because the two used to
// be stated separately -- 32 here, {0,31} in internal/provisioning -- and
// agreed only because 32 == 1+31. Deriving one from the other is what makes
// them keep agreeing.
var validRemoteUser = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,` + strconv.Itoa(maxUsernameLen-1) + `}$`)

// ValidRemoteUser reports whether name is a username the instance can be
// provisioned with.
//
// Exported because internal/provisioning re-checks it before interpolating
// the name into a root-run boot script. That guard is deliberate defence in
// depth and stays; what it stops owning is a second copy of the rule.
func ValidRemoteUser(name string) bool {
	return validRemoteUser.MatchString(name)
}

// RemoteUser derives the instance's non-root login name from the local
// OS user (via os/user, not by shelling out to whoami), sanitized to a
// valid Linux username: any DOMAIN\ prefix stripped, lowercased,
// invalid characters replaced with "-", prefixed with "u" if it
// doesn't start with a letter, and capped at 32 characters. Falls back
// to "bivouac" if nothing usable remains.
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
		return "bivouac"
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = "u" + name
	}
	if len(name) > maxUsernameLen {
		name = name[:maxUsernameLen]
	}
	name = strings.Trim(name, "-")
	// Checked against the same predicate provisioning's guard uses, rather
	// than trusted because the steps above ought to imply it. If some input
	// ever slips through them, the fallback is a working instance with a
	// dull username instead of a render error at provision time.
	if !ValidRemoteUser(name) {
		return "bivouac"
	}
	return name
}
