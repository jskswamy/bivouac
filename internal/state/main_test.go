package state

import (
	"os"
	"testing"

	"github.com/jskswamy/cloudlab/internal/testenv"
)

// Every test in this package starts with an isolated home, so a test that
// forgets to redirect it cannot read or write the developer's real files.
// See internal/testenv.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunMain(m))
}
