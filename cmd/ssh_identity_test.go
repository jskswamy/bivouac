package cmd

import (
	"context"
	"testing"

	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/testenv"
	"github.com/spf13/cobra"
)

func TestResolveInstanceScopesRecordedIdentityWithoutLoadingKeys(t *testing.T) {
	testenv.Isolate(t)
	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "old"} {
		r := state.Record{Name: name, IP: "192.0.2.1", User: "me"}
		if name != "old" {
			r.SSH = &sshidentity.Connection{Name: name, Alias: "bivouac-" + name, Keys: []sshidentity.Identity{{PublicPath: "/missing.pub", PrivatePath: "/missing"}}}
		}
		if err := store.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	for _, name := range []string{"one", "two", "old"} {
		if _, _, err := resolveInstance(cmd, name); err != nil {
			t.Fatal(err)
		}
		c, ok := sshidentity.FromContext(cmd.Context())
		if name == "old" {
			if ok {
				t.Fatal("old record inherited selected keys")
			}
		} else if !ok || c.Name != name {
			t.Fatalf("wrong per-machine context: %+v", c)
		}
	}
}
