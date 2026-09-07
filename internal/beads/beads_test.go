package beads

import "testing"

func TestVersion_ReturnsTheLocalBdBanner(t *testing.T) {
	requireBd(t)
	out, err := Version(t.Context())
	if err != nil {
		t.Fatalf("Version() error = %v", err)
	}
	if versionNumberPattern.FindString(out) == "" {
		t.Errorf("Version() = %q, want it to contain a version number", out)
	}
}

func TestVersion_ErrorsWhenBdIsNotOnPath(t *testing.T) {
	// An empty PATH -- not just a tempdir -- so this cannot accidentally find
	// a real bd on a system PATH entry bash resolves some other way.
	t.Setenv("PATH", t.TempDir())
	if _, err := Version(t.Context()); err == nil {
		t.Fatal("Version() error = nil with no bd on PATH, want an error")
	}
}

func TestVersionsMatch(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{
			name: "identical banners",
			a:    "bd version 1.1.2 (20e493e56: HEAD@20e493e569c9)",
			b:    "bd version 1.1.2 (20e493e56: HEAD@20e493e569c9)",
			want: true,
		},
		{
			// The whole point of comparing the version number rather than the
			// whole banner: every release carries a different build hash even
			// when the version itself has not changed.
			name: "same version number, different build hash",
			a:    "bd version 1.1.2 (aaaaaaaaa: HEAD@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)",
			b:    "bd version 1.1.2 (bbbbbbbbb: HEAD@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)",
			want: true,
		},
		{
			name: "different version numbers",
			a:    "bd version 1.1.2 (aaaaaaaaa: HEAD@aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)",
			b:    "bd version 1.0.3 (bbbbbbbbb: HEAD@bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)",
			want: false,
		},
		{
			name: "an unparseable banner never matches, not even itself",
			a:    "command not found",
			b:    "command not found",
			want: false,
		},
		{
			name: "empty never matches",
			a:    "",
			b:    "",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VersionsMatch(tt.a, tt.b); got != tt.want {
				t.Errorf("VersionsMatch(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
