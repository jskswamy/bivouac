package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEditFixture(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "base.pkl")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetImage_ReplacesOnlyTheImageLine(t *testing.T) {
	before := "// my personal defaults\n" +
		"region = \"blr1\"   // closest\n" +
		"\n" +
		"/* the launch image */\n" +
		"image = \"ubuntu-24-04-x64\" // stock for now\n" +
		"packages { \"ripgrep\" }\n"
	want := "// my personal defaults\n" +
		"region = \"blr1\"   // closest\n" +
		"\n" +
		"/* the launch image */\n" +
		"image = \"tag:my-dev\" // stock for now\n" +
		"packages { \"ripgrep\" }\n"
	path := writeEditFixture(t, before, 0o640)

	got, err := SetImage(path, "tag:my-dev")
	if err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if !got.Written {
		t.Fatalf("outcome = %+v, want Written", got)
	}
	if after := readFile(t, path); after != want {
		t.Errorf("file =\n%s\nwant\n%s", after, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 0640 kept", info.Mode().Perm())
	}
}

func TestSetImage_AppendsWhenAbsent(t *testing.T) {
	for _, tc := range []struct{ name, before, want string }{
		{"trailing newline", "region = \"blr1\"\n", "region = \"blr1\"\nimage = \"tag:my-dev\"\n"},
		{"no trailing newline", "region = \"blr1\"", "region = \"blr1\"\nimage = \"tag:my-dev\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeEditFixture(t, tc.before, 0o600)
			got, err := SetImage(path, "tag:my-dev")
			if err != nil || !got.Written {
				t.Fatalf("SetImage = %+v, %v; want Written", got, err)
			}
			if after := readFile(t, path); after != tc.want {
				t.Errorf("file = %q, want %q", after, tc.want)
			}
		})
	}
}

// Only a top-level declaration is the config's image. One inside an
// object body, a comment or a multi-line string is some other value
// that merely reads the same, and editing it would change the wrong thing.
func TestSetImage_IgnoresNestedAndCommentedImage(t *testing.T) {
	before := "settings {\n" +
		"  image = \"inner\"\n" +
		"}\n" +
		"// image = \"commented\"\n" +
		"/*\n" +
		"image = \"block\"\n" +
		"*/\n" +
		"notes = \"\"\"\n" +
		"image = \"in a string\"\n" +
		"\"\"\"\n"
	path := writeEditFixture(t, before, 0o600)

	got, err := SetImage(path, "tag:my-dev")
	if err != nil || !got.Written {
		t.Fatalf("SetImage = %+v, %v; want Written", got, err)
	}
	if after := readFile(t, path); after != before+"image = \"tag:my-dev\"\n" {
		t.Errorf("file =\n%s\nwant the original with one appended line", after)
	}
}

// A top-level image that is not a plain string literal is an expression
// the user wrote on purpose; rewriting it would discard their logic.
func TestSetImage_RefusesNonLiteralImage(t *testing.T) {
	before := "image = if (true) \"a\" else \"b\"\n"
	path := writeEditFixture(t, before, 0o600)

	got, err := SetImage(path, "tag:my-dev")
	if err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if got.Written || got.Reason == "" {
		t.Errorf("outcome = %+v, want not written with a reason", got)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("file changed to %q", after)
	}
}

// Home Manager links base.pkl into the Nix store. Writing through the
// link fails; replacing it would detach the file from Home Manager.
func TestSetImage_SymlinkIsLeftAlone(t *testing.T) {
	target := writeEditFixture(t, "region = \"blr1\"\n", 0o600)
	link := filepath.Join(t.TempDir(), "base.pkl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got, err := SetImage(link, "tag:my-dev")
	if err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if got != (EditOutcome{Written: false, Reason: SkipSymlink}) {
		t.Errorf("outcome = %+v, want symlink", got)
	}
	if after := readFile(t, target); after != "region = \"blr1\"\n" {
		t.Errorf("target changed to %q", after)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link no longer a symlink: %v", err)
	}
}

func TestSetImage_NixStoreIsLeftAlone(t *testing.T) {
	store := t.TempDir()
	orig := nixStorePrefix
	nixStorePrefix = store + string(filepath.Separator)
	t.Cleanup(func() { nixStorePrefix = orig })

	path := filepath.Join(store, "abc-base.pkl")
	if err := os.WriteFile(path, []byte("region = \"blr1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SetImage(path, "tag:my-dev")
	if err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if got != (EditOutcome{Written: false, Reason: SkipNixStore}) {
		t.Errorf("outcome = %+v, want nix store", got)
	}
	if after := readFile(t, path); after != "region = \"blr1\"\n" {
		t.Errorf("file changed to %q", after)
	}
}

func TestSetImage_ReadOnlyFileIsLeftAlone(t *testing.T) {
	path := writeEditFixture(t, "region = \"blr1\"\n", 0o444)
	got, err := SetImage(path, "tag:my-dev")
	if err != nil {
		t.Fatalf("SetImage: %v", err)
	}
	if got != (EditOutcome{Written: false, Reason: SkipReadOnly}) {
		t.Errorf("outcome = %+v, want read-only", got)
	}
	if after := readFile(t, path); after != "region = \"blr1\"\n" {
		t.Errorf("file changed to %q", after)
	}
}

// A writable file in a directory that refuses the temp file is a real
// failure: the user expected the edit and it did not happen.
func TestSetImage_UnwritableDirIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	path := writeEditFixture(t, "region = \"blr1\"\n", 0o600)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	got, err := SetImage(path, "tag:my-dev")
	if err == nil {
		t.Fatalf("SetImage = %+v, want an error", got)
	}
	if got.Written {
		t.Errorf("outcome = %+v, want not written", got)
	}
	if after := readFile(t, path); after != "region = \"blr1\"\n" {
		t.Errorf("file changed to %q", after)
	}
}
