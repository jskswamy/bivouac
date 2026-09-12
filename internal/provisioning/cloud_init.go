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
	"strings"
	"text/template"

	"github.com/jskswamy/cloudlab/internal/identity"
)

//go:embed cloud-init.sh
var cloudInitTemplateSrc string

var cloudInitTemplate = template.Must(template.New("cloud-init.sh").Parse(cloudInitTemplateSrc))

// RenderCloudInit renders the instance's cloud-init boot script for
// username: installs Nix, creates username as a passwordless-sudo
// user with root's own authorized_keys, enables lingering for it (so
// home-manager's systemd --user services persist), and disables root
// SSH login as the last step -- only once username's key-based login
// is confirmed in place.
func RenderCloudInit(username string) (string, error) {
	// Re-checked rather than trusted, because username is interpolated
	// directly into a root-run boot script -- identity.RemoteUser already
	// sanitizes to this shape, and this is the trust boundary saying so
	// again. It asks identity rather than restating the rule: a second copy
	// is what let 32 and {0,31} drift apart in the first place.
	if !identity.ValidRemoteUser(username) {
		return "", fmt.Errorf("invalid remote username %q", username)
	}
	var b strings.Builder
	if err := cloudInitTemplate.Execute(&b, struct{ Username string }{username}); err != nil {
		return "", err
	}
	return b.String(), nil
}
