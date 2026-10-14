package wizard

import (
	"path/filepath"
	"strings"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshkeys"
)

// KeyChoice's Value is a public-file path. Provider fingerprints are only
// account-matching detail and must never become a configuration value.
type KeyChoice struct {
	Value         string
	Fingerprint   string
	Label         string
	Detail        string
	Local         bool
	Registered    bool
	Selected      bool
	PublicKey     string
	SuggestedName string
}
type KeyOffer struct {
	Choices      []KeyChoice
	AccountKnown bool
}

// OfferKeys excludes account-only and agent-only identities: exporting their
// public material to a file is the explicit route to select them.
func OfferKeys(local []sshkeys.Key, account []provider.SSHKey, accountKnown bool, current []string) KeyOffer {
	registered := map[string]bool{}
	if accountKnown {
		for _, key := range account {
			registered[key.Fingerprint] = true
		}
	}
	inConfig := map[string]bool{}
	for _, path := range current {
		inConfig[path] = true
	}
	offer := KeyOffer{AccountKnown: accountKnown}
	seen := map[string]bool{}
	for _, key := range local {
		if key.Path == "" || seen[key.Path] {
			continue
		}
		seen[key.Path] = true
		onAccount := registered[key.Fingerprint]
		detail := key.Source.String()
		if key.Comment != "" {
			detail += ", " + key.Comment
		}
		if accountKnown {
			if onAccount {
				detail += ", registered"
			} else {
				detail += ", pending registration"
			}
		}
		selected := inConfig[key.Path] || accountKnown && onAccount || !accountKnown && key.Source == sshkeys.Agent
		offer.Choices = append(offer.Choices, KeyChoice{Value: key.Path, Fingerprint: key.Fingerprint, Label: key.Path, Detail: detail, Local: true, Registered: onAccount, Selected: selected, PublicKey: key.PublicKey, SuggestedName: "bivouac-" + strings.TrimSuffix(filepath.Base(key.Path), ".pub")})
	}
	return offer
}

func NeedsUpload(offer KeyOffer, chosen []string) []KeyChoice {
	if !offer.AccountKnown {
		return nil
	}
	picked := map[string]bool{}
	for _, path := range chosen {
		picked[path] = true
	}
	var missing []KeyChoice
	for _, choice := range offer.Choices {
		if picked[choice.Value] && choice.Local && !choice.Registered {
			missing = append(missing, choice)
		}
	}
	return missing
}
