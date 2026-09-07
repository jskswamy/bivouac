package beads

import "testing"

func TestParseRemoteList(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []Remote
	}{
		{
			name: "empty output is no remotes",
			out:  "",
			want: nil,
		},
		{
			name: "a dolthub remote",
			out:  "origin https://doltremoteapi.dolthub.com/jskswamy/cloudlab\n",
			want: []Remote{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"}},
		},
		{
			name: "a session git remote",
			out:  "cloudlab-fix-auth git+ssh://subramk@100.1.2.3/home/subramk/sessions/fix-auth/cloudlab\n",
			want: []Remote{{Name: "cloudlab-fix-auth", URL: "git+ssh://subramk@100.1.2.3/home/subramk/sessions/fix-auth/cloudlab"}},
		},
		{
			name: "several remotes, ragged column alignment",
			out: "origin              https://doltremoteapi.dolthub.com/jskswamy/cloudlab\n" +
				"cloudlab-fix-auth   git+file:///home/subramk/sessions/fix-auth/cloudlab\n",
			want: []Remote{
				{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"},
				{Name: "cloudlab-fix-auth", URL: "git+file:///home/subramk/sessions/fix-auth/cloudlab"},
			},
		},
		{
			name: "blank lines and a header are ignored",
			out:  "NAME URL\n\norigin https://doltremoteapi.dolthub.com/jskswamy/cloudlab\n",
			want: []Remote{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRemoteList(tt.out)
			if len(got) != len(tt.want) {
				t.Fatalf("parseRemoteList() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("remote %d = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		remotes    []Remote
		wantMode   Mode
		wantExtURL string
	}{
		{
			name:     "no remotes is unsynced",
			remotes:  nil,
			wantMode: ModeUnsynced,
		},
		{
			name:     "a git+ssh remote is git mode",
			remotes:  []Remote{{Name: "cloudlab-x", URL: "git+ssh://u@h/p"}},
			wantMode: ModeGit,
		},
		{
			name:     "a git+file remote is git mode",
			remotes:  []Remote{{Name: "cloudlab-x", URL: "git+file:///p"}},
			wantMode: ModeGit,
		},
		{
			name:       "an https remote is external",
			remotes:    []Remote{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"}},
			wantMode:   ModeExternal,
			wantExtURL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab",
		},
		{
			name:       "an aws remote is external",
			remotes:    []Remote{{Name: "origin", URL: "aws://[dynamo:s3]/beads"}},
			wantMode:   ModeExternal,
			wantExtURL: "aws://[dynamo:s3]/beads",
		},
		{
			// The repository this design was written in: DoltHub configured
			// AND a session remote wired. External wins, because the external
			// URL is the one thing the caller cannot reconstruct itself.
			name: "external wins when both are present",
			remotes: []Remote{
				{Name: "cloudlab-x", URL: "git+ssh://u@h/p"},
				{Name: "origin", URL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab"},
			},
			wantMode:   ModeExternal,
			wantExtURL: "https://doltremoteapi.dolthub.com/jskswamy/cloudlab",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.remotes)
			if got.Mode != tt.wantMode {
				t.Errorf("classify().Mode = %v, want %v", got.Mode, tt.wantMode)
			}
			if got.ExternalURL != tt.wantExtURL {
				t.Errorf("classify().ExternalURL = %q, want %q", got.ExternalURL, tt.wantExtURL)
			}
		})
	}
}

func TestDetect_ReportsAbsentWhenTheRepoHasNoBeadsDir(t *testing.T) {
	// Detect returns ModeAbsent when EITHER Present or Available is false, so
	// without this assertion the test would pass on a machine with no bd at
	// all for the wrong reason.
	if !Available() {
		t.Fatal("bd not on PATH; this test needs bd available to prove the .beads check, not bd's absence")
	}
	// t.TempDir() has no .beads/, which is the ModeAbsent case: every beads
	// step downstream is skipped without ever invoking bd.
	got, err := Detect(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect() error = %v, want nil", err)
	}
	if got.Mode != ModeAbsent {
		t.Errorf("Detect().Mode = %v, want ModeAbsent", got.Mode)
	}
}
