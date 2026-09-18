package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/testenv"
)

func TestProvisionCommand_NotInRepoErrors(t *testing.T) {
	chdir(t, t.TempDir())

	root := newRootCmd()
	root.SetArgs([]string{"provision"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "use --repo") {
		t.Errorf("error = %q, want mention of --repo", err.Error())
	}
}

func TestProvisionCommand_InstanceNotFoundErrors(t *testing.T) {
	chdir(t, initTestRepo(t))
	testenv.Isolate(t)

	root := newRootCmd()
	root.SetArgs([]string{"provision"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

func TestProvisionCommand_PositionalNameOverridesDerivedName(t *testing.T) {
	chdir(t, initTestRepo(t))
	testenv.Isolate(t)

	root := newRootCmd()
	root.SetArgs([]string{"provision", "somename"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `"somename"`) {
		t.Errorf("error = %q, want it to name instance %q, not the derived repo name", err.Error(), "somename")
	}
}

// The same divergence as TestUpCommand_NotInRepoErrorsEvenWithAnExplicitName:
// provision resolves bivouac.pkl from the repository root, so an explicitly
// named instance does not excuse it from finding one.
func TestProvisionCommand_NotInRepoErrorsEvenWithAnExplicitName(t *testing.T) {
	for _, args := range [][]string{
		{"provision", "somename"},
		{"provision", "--name", "somename"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			chdir(t, t.TempDir())

			root := newRootCmd()
			root.SetArgs(args)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)

			err := root.Execute()
			if err == nil {
				t.Fatal("expected an error: bivouac.pkl cannot be found without a repository root")
			}
			if !strings.Contains(err.Error(), "use --repo") {
				t.Errorf("error = %q, want mention of --repo", err.Error())
			}
		})
	}
}
