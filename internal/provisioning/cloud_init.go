// Package provisioning produces the artifacts a later sub-project
// needs to bring a cloudlab instance's Nix/home-manager environment
// up to date: the cloud-init payload, template-ref resolution, the
// render-trigger decision, per-instance flake rendering, and offline
// validation. It never touches Pkl, SSH, or a live instance — it only
// ever consumes an already-resolved config.Config.
package provisioning

import _ "embed"

//go:embed cloud-init.sh
var CloudInitUserData string
