package lifecycle

import (
	"context"
	"strings"
	"testing"
)

func TestHerdrArgs_BuildsExpectedCommand(t *testing.T) {
	got := herdrArgs("203.0.113.5", "devuser")
	want := []string{"--remote", "ssh://devuser@203.0.113.5"}
	if len(got) != len(want) {
		t.Fatalf("herdrArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("herdrArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestHerdr_InsideExistingHerdrSession_ReturnsClearErrorWithoutExecing(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")

	err := Herdr(context.Background(), "203.0.113.5", "devuser")
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
