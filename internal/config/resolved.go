package config

import "github.com/jskswamy/bivouac/internal/sshkeys"

// Resolved is a configuration that has been through Resolve: the required
// fields are known to be set, and carried as plain strings rather than the
// pointers the generated struct uses.
//
// The embedded Config is the whole of what bivouac.pkl declares, so a field
// added to the schema reaches callers without being restated here. Only the
// fields whose value Resolve actually settles are lifted out, and they
// deliberately shadow their pointer-shaped counterparts: cfg.Region is the
// string, and cfg.Config.Region is the *string it came from.
//
// That shadowing is the point. Six call sites used to write *cfg.Region on
// the strength of a guarantee that lived in a comment and in validate's
// implementation; now the guarantee is in the type, and the code that
// assumed it either says so or does not compile.
type Resolved struct {
	Config

	// Provider is "digitalocean" or "gcloud".
	Provider string

	// GCloud holds Google Cloud specific configuration if Provider is "gcloud".
	GCloud *GCloudConfig

	// DigitalOcean holds DigitalOcean specific configuration if Provider is "digitalocean".
	DigitalOcean *DigitalOceanConfig

	// Region, Size and Template are non-empty for any Resolved that came
	// from Resolve. Nothing else constructs one in production.
	Region   string
	Size     string
	Template string

	// Shell is fish, zsh or bash: the project's value, else the base's,
	// else fish. Never empty.
	Shell string

	// SSHKeyFiles retains each entry's declaring file through the additive merge.
	SSHKeyFiles []sshkeys.FileRef
}

// defaultShell applies when neither the project nor the base names one.
const defaultShell = "fish"

// resolved lifts a validated Config into a Resolved.
//
// Unexported, and called only after validate has returned nil: a Resolved
// built any other way carries no guarantee, so there is deliberately no
// exported way to make one out of an unchecked Config.
func resolved(c Config) Resolved {
	shell := defaultShell
	if c.Shell != nil {
		shell = *c.Shell
	}

	provider := c.Provider
	if provider == "" {
		provider = "digitalocean"
	}

	region := ""
	size := ""
	var doConfig *DigitalOceanConfig

	switch provider {
	case "gcloud":
		if c.Gcloud != nil {
			region = c.Gcloud.Zone
			size = c.Gcloud.MachineType
			if c.Image != "ubuntu-24-04-x64" {
				// User explicitly configured top-level image (e.g. tag:...)
			} else if c.Gcloud.Image != "" {
				c.Image = c.Gcloud.Image
			}
		}
		if region == "" && c.Region != nil && *c.Region != "" {
			region = *c.Region
		}
		if size == "" && c.Size != nil && *c.Size != "" {
			size = *c.Size
		}
	case "digitalocean":
		if c.Region != nil {
			region = *c.Region
		}
		if c.Digitalocean != nil && c.Digitalocean.Region != nil {
			region = *c.Digitalocean.Region
		}
		if c.Size != nil {
			size = *c.Size
		}
		if c.Digitalocean != nil && c.Digitalocean.Size != nil {
			size = *c.Digitalocean.Size
		}
		if c.Digitalocean != nil {
			doConfig = &DigitalOceanConfig{
				Region: coalesce(c.Digitalocean.Region, c.Region),
				Size:   coalesce(c.Digitalocean.Size, c.Size),
				Image:  coalesce(c.Digitalocean.Image, &c.Image),
			}
		} else {
			doConfig = &DigitalOceanConfig{
				Region: c.Region,
				Size:   c.Size,
				Image:  &c.Image,
			}
		}
		if doConfig.Image != nil && *doConfig.Image != "" {
			c.Image = *doConfig.Image
		}
	}

	template := ""
	if c.Template != nil {
		template = *c.Template
	}

	return Resolved{
		Config:       c,
		Provider:     provider,
		GCloud:       c.Gcloud,
		DigitalOcean: doConfig,
		Region:       region,
		Size:         size,
		Template:     template,
		Shell:        shell,
	}
}
