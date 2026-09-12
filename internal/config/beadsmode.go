package config

// BeadsMode is how an instance's beads issue database syncs, as declared by
// the `beads` field in cloudlab.pkl.
//
// Hand-written beside the generated Config.pkl.go rather than in it: that
// file says DO NOT EDIT and pkl-gen-go emits the Pkl union "session" |
// "dolthub" | "off" as a bare string. So Config.Beads stays a string and
// this is the type everything downstream of it speaks, converted once where
// the config is read.
//
// internal/beads already models the sibling concept -- how the repository
// actually syncs -- as a typed Mode. This is the same idea applied to what
// the user asked for, so a typo in a comparison fails to compile instead of
// silently disabling the feature.
type BeadsMode string

const (
	// BeadsSession is the default: dolt data rides refs/dolt/data on the
	// session's own git remote over SSH, and no credential reaches the
	// instance.
	BeadsSession BeadsMode = "session"
	// BeadsDolthub additionally syncs against the external remote the
	// repository already has configured.
	BeadsDolthub BeadsMode = "dolthub"
	// BeadsOff wires beads not at all.
	BeadsOff BeadsMode = "off"
)
