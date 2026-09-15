package agentcontext

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
)

func TestGlobalPaths_MapsEachHarnessToItsGlobalFile(t *testing.T) {
	paths, unreachable := GlobalPaths([]string{"claude", "codex", "opencode", "copilot", "pi"})
	want := []string{
		".claude/CLAUDE.md",
		".codex/AGENTS.md",
		".config/opencode/AGENTS.md",
		".copilot/copilot-instructions.md",
		".pi/agent/AGENTS.md",
	}
	if !reflect.DeepEqual(want, paths) {
		t.Errorf("GlobalPaths() paths = %#v, want %#v", paths, want)
	}
	if len(unreachable) != 0 {
		t.Errorf("GlobalPaths() unreachable = %v, want none", unreachable)
	}
}

// Cursor's cross-project rules live in GUI User Rules, so there is no
// file to write. Reaching it would mean writing into the checkout, which
// this design refuses -- so it is named rather than silently skipped: an
// instruction that never arrived looks exactly like one that was ignored.
func TestGlobalPaths_NamesCursorAsUnreachable(t *testing.T) {
	paths, unreachable := GlobalPaths([]string{"claude", "cursor"})
	if !reflect.DeepEqual([]string{".claude/CLAUDE.md"}, paths) {
		t.Errorf("GlobalPaths() paths = %#v", paths)
	}
	if !reflect.DeepEqual([]string{"cursor"}, unreachable) {
		t.Errorf("GlobalPaths() unreachable = %#v", unreachable)
	}
}

func TestGlobalPaths_IgnoresUnknownNames(t *testing.T) {
	paths, unreachable := GlobalPaths([]string{"nonesuch"})
	if len(paths) != 0 || len(unreachable) != 0 {
		t.Errorf("GlobalPaths() = %v, %v, want empty", paths, unreachable)
	}
}

// Every name Config.pkl accepts must be either mapped or explicitly
// unreachable. A new harness added to the schema and forgotten here would
// silently receive no instructions.
func TestGlobalPaths_CoversEveryAgentInTheSchema(t *testing.T) {
	for _, name := range agentsUnionFromSchema(t) {
		if _, ok := globalPaths[name]; ok {
			continue
		}
		if unreachableHarnesses[name] {
			continue
		}
		t.Errorf("agent %q is in Config.pkl but neither mapped nor listed unreachable", name)
	}
}

// agentsUnionFromSchema reads Config.pkl's `agents` union directly, so
// this test fails the moment the schema and this package's harness map
// drift apart. Adapted from internal/wizard/wizard_test.go's equivalent
// helpers, which guard the wizard's option list the same way.
func agentsUnionFromSchema(t *testing.T) []string {
	t.Helper()
	schema := readSchema(t)
	return unionMembers(t, schema, config.FieldAgents)
}

func readSchema(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(thisFile), "..", "config", "Config.pkl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading Config.pkl: %v", err)
	}
	return string(raw)
}

// unionMembers pulls the quoted members of `field: "a"|"b"|...` out of
// the schema text.
func unionMembers(t *testing.T, schema, field string) []string {
	t.Helper()
	line := regexp.MustCompile(`(?m)^` + field + `:.*$`).FindString(schema)
	if line == "" {
		t.Fatalf("no declaration of %q in Config.pkl", field)
	}
	// Everything after the first `=` is the field's default, not a
	// member of its union.
	if i := strings.Index(line, "="); i >= 0 {
		line = line[:i]
	}

	var members []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(line, -1) {
		members = append(members, m[1])
	}
	if len(members) == 0 {
		t.Fatalf("no union members on %q's declaration", field)
	}
	return members
}
