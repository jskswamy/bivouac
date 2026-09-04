package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirm_AcceptsYAndYesCaseInsensitive(t *testing.T) {
	for _, in := range []string{"y\n", "Y\n", "yes\n", "YES\n", "  yes  \n"} {
		root := newRootCmd()
		root.SetIn(strings.NewReader(in))
		var out bytes.Buffer
		root.SetOut(&out)

		ok, err := confirm(root, "Proceed? [y/N]: ")
		if err != nil {
			t.Fatalf("confirm(%q) error = %v", in, err)
		}
		if !ok {
			t.Errorf("confirm(%q) = false, want true", in)
		}
	}
}

func TestConfirm_RejectsAnythingElse(t *testing.T) {
	for _, in := range []string{"n\n", "no\n", "\n", "maybe\n"} {
		root := newRootCmd()
		root.SetIn(strings.NewReader(in))
		var out bytes.Buffer
		root.SetOut(&out)

		ok, err := confirm(root, "Proceed? [y/N]: ")
		if err != nil {
			t.Fatalf("confirm(%q) error = %v", in, err)
		}
		if ok {
			t.Errorf("confirm(%q) = true, want false", in)
		}
	}
}

func TestConfirm_EmptyStdin_TreatedAsNo(t *testing.T) {
	root := newRootCmd()
	root.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	root.SetOut(&out)

	ok, err := confirm(root, "Proceed? [y/N]: ")
	if err != nil {
		t.Fatalf("confirm() error = %v", err)
	}
	if ok {
		t.Error("confirm() on EOF = true, want false")
	}
}

func TestConfirm_PrintsPromptToCommandOutput(t *testing.T) {
	root := newRootCmd()
	root.SetIn(strings.NewReader("y\n"))
	var out bytes.Buffer
	root.SetOut(&out)

	if _, err := confirm(root, "Proceed? [y/N]: "); err != nil {
		t.Fatalf("confirm() error = %v", err)
	}
	if !strings.Contains(out.String(), "Proceed? [y/N]: ") {
		t.Errorf("output = %q, want it to contain the prompt", out.String())
	}
}
