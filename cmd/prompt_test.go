package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
	"github.com/jskswamy/cloudlab/internal/wizard"
)

// answerValidator is the one piece of the form layer that can be tested
// without a terminal, and the one that most needs it.
//
// huh hands back whatever was typed on the line, which for a TextList is
// the whole comma-separated list rather than one answer. A question's
// validator is written against a single value, so the adapter has to
// split first -- and getting that wrong is invisible: every one-entry
// answer still passes, and the scripted prompter the flow tests drive
// never calls a validator at all. So this asserts what the validator was
// handed, not merely whether the line was accepted.
func TestAnswerValidator_ChecksEachEntryOfAList(t *testing.T) {
	tests := []struct {
		name    string
		kind    wizard.Kind
		line    string
		wantSaw []string
	}{
		{
			name:    "a list is split into its entries",
			kind:    wizard.TextList,
			line:    "a.md, b.md",
			wantSaw: []string{"a.md", "b.md"},
		},
		{
			name:    "whitespace alone separates too",
			kind:    wizard.TextList,
			line:    "a.md b.md",
			wantSaw: []string{"a.md", "b.md"},
		},
		{
			name:    "one entry is still one entry",
			kind:    wizard.TextList,
			line:    "a.md",
			wantSaw: []string{"a.md"},
		},
		{
			// Blank means none, so there is nothing to check and the
			// validator is never reached.
			name:    "a blank list has nothing to check",
			kind:    wizard.TextList,
			line:    "   ",
			wantSaw: nil,
		},
		{
			// A Text answer is one value already; it is only trimmed,
			// because a stray space must not reach the validator.
			name:    "a text answer arrives whole and trimmed",
			kind:    wizard.Text,
			line:    "  nyc3  ",
			wantSaw: []string{"nyc3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var saw []string
			validate := answerValidator(wizard.Question{
				Kind: tt.kind,
				Validate: func(v string) error {
					saw = append(saw, v)
					return nil
				},
			})
			if err := validate(tt.line); err != nil {
				t.Fatalf("validate(%q) = %v, want nil", tt.line, err)
			}
			if !reflect.DeepEqual(saw, tt.wantSaw) {
				t.Errorf("validator saw %#v, want %#v", saw, tt.wantSaw)
			}
		})
	}
}

// The case that proves the split rather than merely surviving it: the
// entry that is wrong is the second one, so a validator handed the raw
// line would report "a.md, b.md" -- naming a file nobody typed and
// leaving the user to work out which half is at fault.
func TestAnswerValidator_NamesTheEntryThatFailed(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.md", "ok.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The real question, so this also pins the two halves together: the
	// wizard's validator and the form layer's splitting of the line.
	validate := answerValidator(instructionsQuestion(t, dir))

	if err := validate("a.md, ok.md"); err != nil {
		t.Errorf("validate(%q) = %v, want nil -- both files exist", "a.md, ok.md", err)
	}
	if err := validate(""); err != nil {
		t.Errorf("validate(\"\") = %v, want nil -- blank means none", err)
	}

	err := validate("a.md, gone.md")
	if err == nil {
		t.Fatal("validate(\"a.md, gone.md\") = nil, want an error for the second entry")
	}
	if !strings.Contains(err.Error(), "gone.md") {
		t.Errorf("error %q does not name the entry that failed", err)
	}
	// Whole-line validation would put the entire answer in the message,
	// which reads as one impossible filename.
	if strings.Contains(err.Error(), "a.md,") {
		t.Errorf("error %q reports the whole line rather than the entry", err)
	}
}

// A question with nothing to check must produce no hook at all, so huh
// is not handed a function that always says yes.
func TestAnswerValidator_IsNilWithoutAValidator(t *testing.T) {
	if got := answerValidator(wizard.Question{Kind: wizard.TextList}); got != nil {
		t.Error("answerValidator() returned a hook for a question with no validator")
	}
}

// The first failing entry stops the check: the message names one file,
// and a list where several are missing is fixed one at a time anyway.
func TestAnswerValidator_StopsAtTheFirstFailure(t *testing.T) {
	var saw []string
	validate := answerValidator(wizard.Question{
		Kind: wizard.TextList,
		Validate: func(v string) error {
			saw = append(saw, v)
			if v == "bad.md" {
				return fmt.Errorf("%s: no", v)
			}
			return nil
		},
	})
	if err := validate("good.md, bad.md, later.md"); err == nil {
		t.Fatal("validate() = nil, want the failure from bad.md")
	}
	if want := []string{"good.md", "bad.md"}; !reflect.DeepEqual(saw, want) {
		t.Errorf("validator saw %#v, want %#v", saw, want)
	}
}

// instructionsQuestion is the real personal question for instructions,
// with its paths resolved against dir.
func instructionsQuestion(t *testing.T, dir string) wizard.Question {
	t.Helper()
	for _, q := range wizard.PersonalQuestions(dir) {
		if q.Field == config.FieldInstructions {
			return q
		}
	}
	t.Fatal("no instructions question")
	return wizard.Question{}
}
