package reconcile

import (
	"os"
	"testing"

	"github.com/jskswamy/cloudlab/internal/testenv"
)

// Every test in this package starts with an isolated home.
//
// Not a convenience: this package connects over SSH, and reconcile.Connect
// records accepted host keys in $HOME/.ssh/known_hosts. With isolation left
// to each test to remember, the ones that forgot wrote to the developer's
// real file -- and once the OS recycled one of those ephemeral ports, the
// suite failed with a host key mismatch unrelated to the code under test.
func TestMain(m *testing.M) {
	os.Exit(testenv.RunMain(m))
}
