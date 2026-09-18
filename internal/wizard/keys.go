package wizard

import (
	"strings"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/sshkeys"
)

// KeyChoice is one SSH key the sshKeys question can offer.
type KeyChoice struct {
	// Fingerprint is what gets written to the config.
	Fingerprint string
	// Label is the human name: the key's comment, or the fingerprint
	// itself when that is all there is.
	Label string
	// Detail is the line under the label saying where the key is and
	// whether the account has it.
	Detail string
	// Local reports whether this machine holds the key.
	Local bool
	// Registered reports whether the provider account has it. Only
	// meaningful when the offer's AccountKnown is true.
	Registered bool
	// Selected is the default.
	Selected bool
	// PublicKey is the authorized_keys line, for registering the key.
	// Empty for a key this machine does not hold.
	PublicKey string
	// SuggestedName is the default name to register the key under.
	SuggestedName string
}

// KeyOffer is the whole set of choices for the sshKeys question.
type KeyOffer struct {
	Choices []KeyChoice
	// AccountKnown reports whether the provider's keys were read. When
	// false, nothing can say whether a key is registered -- and nothing
	// should claim to.
	AccountKnown bool

	// registered is every fingerprint the account holds.
	//
	// Kept whole rather than only on the choices, because the answer can
	// contain fingerprints that were never offered: the free-text entry
	// appends one after the form has closed. Checking the choices alone
	// would leave exactly that route unverified.
	registered map[string]bool
}

// Unregistered returns the chosen fingerprints the account does not
// hold, in the order given.
//
// Covers what NeedsUpload cannot: a fingerprint typed by hand, and one
// carried in from an existing config. Neither can be uploaded from here
// -- there is no public key for either -- but writing one the account
// lacks makes every later `up` fail at create, with a message that has
// been known to blame the hostname instead. Naming it now is the only
// warning the user gets.
//
// Empty when the account was never read: nothing can be known missing
// from a list that was not fetched.
func (o KeyOffer) Unregistered(chosen []string) []string {
	if !o.AccountKnown {
		return nil
	}
	var out []string
	for _, fp := range chosen {
		if !o.registered[fp] {
			out = append(out, fp)
		}
	}
	return out
}

// OfferKeys assembles the sshKeys choices from what this machine holds,
// what the account holds, and what the config already names.
//
// account is only consulted when accountKnown; passing an empty slice
// with accountKnown true means the account genuinely has no keys, which
// is different from not having looked.
//
// Ordering is local keys first in discovery order (agent before disk),
// then account keys this machine does not hold, then any fingerprint the
// config names that matched nothing.
func OfferKeys(local []sshkeys.Key, account []provider.SSHKey, accountKnown bool, current []string) KeyOffer {
	registered := make(map[string]provider.SSHKey, len(account))
	if accountKnown {
		for _, k := range account {
			registered[k.Fingerprint] = k
		}
	}
	inConfig := make(map[string]bool, len(current))
	for _, fp := range current {
		inConfig[fp] = true
	}

	offer := KeyOffer{AccountKnown: accountKnown, registered: map[string]bool{}}
	for fp := range registered {
		offer.registered[fp] = true
	}
	seen := map[string]bool{}

	// A smartcard's comment is its serial number, which names the card,
	// not the key slot: a card exposing more than one SSH-capable key
	// (e.g. separate authentication and signature slots) reports the
	// same comment for both. Counted up front so two such choices can be
	// told apart -- picking "the" entry among two identical-looking ones
	// is a guess, not a choice.
	labelCount := make(map[string]int, len(local))
	for _, k := range local {
		labelCount[k.Comment]++
	}

	// Any local key is one the user demonstrably holds -- the thing that
	// actually prevents an unreachable instance.
	for _, k := range local {
		_, onAccount := registered[k.Fingerprint]
		label := disambiguateLabel(k.Comment, k.Fingerprint, labelCount[k.Comment])
		offer.Choices = append(offer.Choices, KeyChoice{
			Fingerprint:   k.Fingerprint,
			Label:         label,
			Detail:        localDetail(k, accountKnown, onAccount),
			Local:         true,
			Registered:    onAccount,
			Selected:      selectLocal(k, accountKnown, onAccount, inConfig[k.Fingerprint]),
			PublicKey:     k.PublicKey,
			SuggestedName: label,
		})
		seen[k.Fingerprint] = true
	}

	// Account keys with no local half. Offered because setting up a
	// second machine or a colleague is real, never preselected because
	// choosing one is how an instance ends up with a key nobody here
	// holds -- which boots, bills, and refuses the login.
	for _, k := range account {
		if seen[k.Fingerprint] {
			continue
		}
		offer.Choices = append(offer.Choices, KeyChoice{
			Fingerprint: k.Fingerprint,
			Label:       accountLabel(k),
			Detail:      "on your account, not on this machine",
			Registered:  true,
			Selected:    inConfig[k.Fingerprint],
		})
		seen[k.Fingerprint] = true
	}

	// A fingerprint the config names that matched nothing. Kept, and
	// kept selected: dropping it would mean editing one setting quietly
	// removed a key.
	for _, fp := range current {
		if seen[fp] {
			continue
		}
		offer.Choices = append(offer.Choices, KeyChoice{
			Fingerprint: fp,
			Label:       fp,
			Detail:      "from your config; not found on this machine" + accountSuffix(accountKnown),
			Selected:    true,
		})
		seen[fp] = true
	}

	return offer
}

// selectLocal decides the default for a key this machine holds.
//
// With the account known, the keys certain to work are the ones already
// registered. Without it, the agent's keys are the best evidence
// available: a key loaded into an agent is one in active use. Disk-only
// keys are left for the user to pick, because selecting a stale one
// costs every later `up` a 422 until the config is edited again.
func selectLocal(k sshkeys.Key, accountKnown, onAccount, inConfig bool) bool {
	if inConfig {
		return true
	}
	if accountKnown {
		return onAccount
	}
	return k.Source == sshkeys.Agent
}

// disambiguateLabel appends a short fingerprint suffix to label when
// count local keys share it, so two otherwise-identical-looking choices
// can be told apart in the picker and, if uploaded, on the account.
func disambiguateLabel(label, fingerprint string, count int) string {
	if count <= 1 {
		return label
	}
	suffix := fingerprint
	if parts := strings.Split(fingerprint, ":"); len(parts) >= 2 {
		suffix = strings.Join(parts[len(parts)-2:], ":")
	}
	return label + " (" + suffix + ")"
}

func localDetail(k sshkeys.Key, accountKnown, onAccount bool) string {
	where := k.Source.String()
	if !accountKnown {
		return where
	}
	if onAccount {
		return where + ", registered"
	}
	return where + ", not on your account"
}

func accountLabel(k provider.SSHKey) string {
	if name := strings.TrimSpace(k.Name); name != "" {
		return name
	}
	return k.Fingerprint
}

func accountSuffix(accountKnown bool) string {
	if accountKnown {
		return " or your account"
	}
	return ""
}

// NeedsUpload returns the chosen keys that this machine holds but the
// account does not, in the order they were offered.
//
// Empty when the account was never read: nothing can be known to be
// missing from a list that was not fetched.
func NeedsUpload(offer KeyOffer, chosen []string) []KeyChoice {
	if !offer.AccountKnown {
		return nil
	}
	picked := make(map[string]bool, len(chosen))
	for _, fp := range chosen {
		picked[fp] = true
	}

	var out []KeyChoice
	for _, c := range offer.Choices {
		if picked[c.Fingerprint] && c.Local && !c.Registered {
			out = append(out, c)
		}
	}
	return out
}
