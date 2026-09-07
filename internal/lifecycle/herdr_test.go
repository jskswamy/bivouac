package lifecycle

import (
	"context"
	"strings"
	"testing"
)

// A named herdr session per cloudlab session, same idea as tmux: reconnecting
// to "auth" should land back in the same herdr session rather than
// everything sharing one anonymous session. Empty session must emit exactly
// today's args -- an instance with no session resolved behaves unchanged.
func TestHerdrArgs_BuildsExpectedCommand(t *testing.T) {
	cases := []struct {
		name    string
		session string
		want    []string
	}{
		{"no session", "", []string{"--remote", "ssh://devuser@203.0.113.5"}},
		{"with session", "auth", []string{"--remote", "ssh://devuser@203.0.113.5", "--session", "auth"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := herdrArgs("203.0.113.5", "devuser", c.session)
			if len(got) != len(c.want) {
				t.Fatalf("herdrArgs() = %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("herdrArgs()[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestHerdr_InsideExistingHerdrSession_ReturnsClearErrorWithoutExecing(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")

	err := Herdr(context.Background(), "203.0.113.5", "devuser", "")
	if err == nil {
		t.Fatal("Herdr() error = nil, want an error when already inside a herdr session")
	}
	if !strings.Contains(err.Error(), "nested") && !strings.Contains(err.Error(), "already inside a herdr session") {
		t.Errorf("error = %q, want it to explain nesting is the problem", err.Error())
	}
	if !strings.Contains(err.Error(), "cloudlab ssh") {
		t.Errorf("error = %q, want it to suggest an alternative", err.Error())
	}
}
