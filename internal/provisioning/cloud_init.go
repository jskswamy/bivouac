// Package provisioning produces the artifacts a later sub-project
// needs to bring a cloudlab instance's Nix/home-manager environment
// up to date: the cloud-init payload, template-ref resolution, the
// render-trigger decision, per-instance flake rendering, and offline
// validation. It never touches Pkl, SSH, or a live instance — it only
// ever consumes an already-resolved config.Config.
package provisioning

import (
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed cloud-init.sh
var cloudInitTemplateSrc string

var cloudInitTemplate = template.Must(template.New("cloud-init.sh").Parse(cloudInitTemplateSrc))

// validRemoteUser matches a valid Linux username: identity.RemoteUser
// already sanitizes to this shape, but RenderCloudInit checks it again
// rather than trusting the caller, since this value is interpolated
// directly into a root-run boot script.
var validRemoteUser = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// RenderCloudInit renders the instance's cloud-init boot script for
// username: installs Nix, creates username as a passwordless-sudo
// user with root's own authorized_keys, enables lingering for it (so
// home-manager's systemd --user services persist), and disables root
// SSH login as the last step -- only once username's key-based login
// is confirmed in place.
func RenderCloudInit(username string) (string, error) {
	if !validRemoteUser.MatchString(username) {
		return "", fmt.Errorf("invalid remote username %q", username)
	}
	var b strings.Builder
	if err := cloudInitTemplate.Execute(&b, struct{ Username string }{username}); err != nil {
		return "", err
	}
	return b.String(), nil
}
