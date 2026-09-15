package agentcontext

// globalPaths maps a config `agents` entry to the file that harness reads
// for instructions that apply to every project, relative to the remote
// home directory.
//
// The global slot rather than anything in or above the checkout, because
// it is the only mechanism the harnesses agree on. Per-directory discovery
// is not portable: Claude Code and opencode walk upward from the working
// directory, while Codex begins its chain at the git repository root and
// walks down, so a file above the checkout reaches two of them and is
// invisible to the third.
//
// Keep in step with Config.pkl's agents union. A test reads the schema and
// fails if the two sets drift.
var globalPaths = map[string]string{
	"claude":   ".claude/CLAUDE.md",
	"codex":    ".codex/AGENTS.md",
	"opencode": ".config/opencode/AGENTS.md",
	"copilot":  ".copilot/copilot-instructions.md",
	"pi":       ".pi/agent/AGENTS.md",
}

// unreachableHarnesses have no global instruction file to write.
//
// Cursor's CLI reads AGENTS.md and CLAUDE.md at the project root and
// .cursor/rules, but cross-project instructions live in User Rules, set in
// the GUI. Delivering to it would mean writing into the checkout, which
// this design refuses.
var unreachableHarnesses = map[string]bool{
	"cursor": true,
}

// GlobalPaths returns the remote path for each named harness that has one,
// and the names of those that do not.
//
// Both results follow the caller's order rather than the map's, so a
// rendered warning and a written file list read the way the config does.
func GlobalPaths(agents []string) (paths []string, unreachable []string) {
	for _, a := range agents {
		if p, ok := globalPaths[a]; ok {
			paths = append(paths, p)
			continue
		}
		if unreachableHarnesses[a] {
			unreachable = append(unreachable, a)
		}
	}
	return paths, unreachable
}
