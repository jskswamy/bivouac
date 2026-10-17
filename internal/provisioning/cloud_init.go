// Package provisioning produces the artifacts a later sub-project
// needs to bring a bivouac instance's Nix/home-manager environment
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

	"github.com/jskswamy/bivouac/internal/identity"
)

//go:embed cloud-init.sh
var cloudInitTemplateSrc string

//go:embed bootstrap.sh
var bootstrapTemplateSrc string

// One template set, so cloud-init.sh can pull in the bootstrap by name and
// a stock launch runs exactly the software setup an image build bakes.
var cloudInitTemplate = template.Must(template.Must(
	template.New("cloud-init.sh").Parse(cloudInitTemplateSrc)).
	New("bootstrap.sh").Parse(bootstrapTemplateSrc))

// BootMode says what the launch image already carries, and so how much of
// the boot script has to run.
type BootMode int

const (
	// BootStock is a stock distribution image: install Nix and the login
	// shell, then set up access.
	BootStock BootMode = iota
	// BootBaked is an image built by bivouac, which already has Nix and the
	// login shell: set up access only. Rerunning the installers there would
	// at best waste the boot and at worst fight the installed Nix.
	BootBaked
)

// RenderCloudInit renders the instance's cloud-init boot script for
// username: in BootStock mode installs Nix and the login shell first, then
// creates username as a passwordless-sudo user with root's own
// authorized_keys and shell as its login shell, enables lingering for it
// (so home-manager's systemd --user services persist), and disables root
// SSH login as the last step -- only once username's key-based login is
// confirmed in place. BootBaked runs the same access setup but only
// detects the shell the image was built with.
//
// shell is fish, zsh or bash, and must be one of them: it is
// interpolated into a root-run boot script, and an empty value is an
// error rather than bash so a config that lost the field fails here
// instead of quietly producing a bash instance.
func RenderCloudInit(username, shell string, mode BootMode) (string, error) {
	if err := validShell(shell); err != nil {
		return "", err
	}
	// Re-checked rather than trusted, because username is interpolated
	// directly into a root-run boot script -- identity.RemoteUser already
	// sanitizes to this shape, and this is the trust boundary saying so
	// again. It asks identity rather than restating the rule: a second copy
	// is what let 32 and {0,31} drift apart in the first place.
	if !identity.ValidRemoteUser(username) {
		return "", fmt.Errorf("invalid remote username %q", username)
	}
	switch mode {
	case BootStock, BootBaked:
	default:
		return "", fmt.Errorf("invalid boot mode %d", mode)
	}
	var b strings.Builder
	data := struct {
		Username, Shell string
		Baked           bool
	}{username, shell, mode == BootBaked}
	if err := cloudInitTemplate.ExecuteTemplate(&b, "cloud-init.sh", data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// RenderBootstrap renders the software bootstrap alone -- Nix and the login
// shell, as a standalone script -- for an image build to run as its
// builder's user data. It carries no account or SSH setup: the builder's
// access is Packer's, and the real account is created at launch.
func RenderBootstrap(shell string) (string, error) {
	if err := validShell(shell); err != nil {
		return "", err
	}
	var b strings.Builder
	if err := cloudInitTemplate.ExecuteTemplate(&b, "bootstrap-script", struct{ Shell string }{shell}); err != nil {
		return "", err
	}
	return b.String(), nil
}

func validShell(shell string) error {
	switch shell {
	case "fish", "zsh", "bash":
		return nil
	}
	return fmt.Errorf("invalid login shell %q: want fish, zsh or bash", shell)
}
