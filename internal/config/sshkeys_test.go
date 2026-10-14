package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/sshkeys"
)

func TestResolveSSHKeys_RejectsLegacyReferences(t *testing.T) {
	entries := []string{"12345", "MD5:10:3e:47:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66", "10:3e:47:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66", "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	for _, entry := range entries {
		t.Run(entry, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
			path := filepath.Join(dir, "bivouac.pkl")
			writeFixture(t, path, fmt.Sprintf("region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\ntemplate = \"minimal\"\nsshKeys { \"keys/local.pub\"; %q }\n", entry))
			_, err := Resolve(context.Background(), path)
			if err == nil {
				t.Fatal("expected migration error")
			}
			for _, want := range []string{entry, path, "public-key file paths only", "~/.ssh/id_ed25519.pub", "bivouac init"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestResolveSSHKeys_DeclaringFileOrigins(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "personal")
	projectDir := filepath.Join(dir, "project")
	for _, path := range []string{baseDir, projectDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(baseDir, "base.pkl")
	project := filepath.Join(projectDir, "bivouac.pkl")
	writeFixture(t, base, "region = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\nsshKeys { \"keys/base.pub\" }\n")
	writeFixture(t, project, fmt.Sprintf("basePath = %q\ntemplate = \"minimal\"\nsshKeys { \"keys/project.pub\" }\n", base))
	cfg, err := Resolve(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	want := []sshkeys.FileRef{
		{Entry: "keys/base.pub", ConfigPath: base, Path: filepath.Join(baseDir, "keys/base.pub")},
		{Entry: "keys/project.pub", ConfigPath: project, Path: filepath.Join(projectDir, "keys/project.pub")},
	}
	if !reflect.DeepEqual(cfg.SSHKeyFiles, want) {
		t.Fatalf("key files = %+v, want %+v", cfg.SSHKeyFiles, want)
	}
	if !reflect.DeepEqual(*cfg.SshKeys, []string{"keys/base.pub", "keys/project.pub"}) {
		t.Fatalf("raw keys changed: %v", *cfg.SshKeys)
	}
}

func TestResolveSSHKeys_PathForms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "base.pkl")
	entries := []string{"~/.ssh/custom key.pub", "./12345", "keys/key with spaces.pub", "$HOME/key.pub"}
	refs, err := resolveSSHKeyFiles(path, entries)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, ".ssh/custom key.pub"), filepath.Join(filepath.Dir(path), "12345"), filepath.Join(filepath.Dir(path), "keys/key with spaces.pub"), filepath.Join(filepath.Dir(path), "$HOME/key.pub")}
	for i, ref := range refs {
		if ref.Path != want[i] || ref.Entry != entries[i] || ref.ConfigPath != path {
			t.Errorf("ref = %+v, want path %q", ref, want[i])
		}
	}
}

func TestResolveSSHKeys_ReportsBothOrigins(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.pkl")
	project := filepath.Join(dir, "bivouac.pkl")
	writeFixture(t, base, "sshKeys { \"12345\" }\n")
	writeFixture(t, project, "basePath = \"./base.pkl\"\nregion = \"nyc3\"\nsize = \"s-1vcpu-1gb\"\ntemplate = \"minimal\"\nsshKeys { \"67890\" }\n")
	_, err := Resolve(context.Background(), project)
	if err == nil {
		t.Fatal("expected migration errors")
	}
	for _, want := range []string{base, project, "12345", "67890"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestReadValues_LegacyKeysRemainEditable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.pkl")
	writeFixture(t, path, "sshKeys { \"12345\" }\n")
	values, err := ReadValues(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got := values[FieldSSHKeys]; fmt.Sprint(got) != "[12345]" {
		t.Fatalf("raw keys = %v", got)
	}
}
