package cmd

import (
	"fmt"
	"os"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/provider/digitalocean"
	"github.com/jskswamy/cloudlab/internal/state"
	"github.com/spf13/cobra"
)

// resolveInstance opens the state store, looks up name, and returns
// the same "no instance named" error every command below list/up
// reports identically when the name doesn't resolve to a known
// instance.
func resolveInstance(name string) (*state.Store, state.Record, error) {
	store, err := state.Open()
	if err != nil {
		return nil, state.Record{}, err
	}
	record, ok, err := store.Get(name)
	if err != nil {
		return nil, state.Record{}, err
	}
	if !ok {
		return nil, state.Record{}, fmt.Errorf("no instance named %q (run cloudlab up first)", name)
	}
	return store, record, nil
}

// resolveProvider builds a DigitalOcean provider from
// DIGITALOCEAN_TOKEN, for commands that need to call the live API
// (down, status).
func resolveProvider() (provider.Provider, error) {
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("DIGITALOCEAN_TOKEN not set")
	}
	return digitalocean.New(token), nil
}

func runDown(cmd *cobra.Command, name string, args []string) error {
	store, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	p, err := resolveProvider()
	if err != nil {
		return err
	}
	if err := lifecycle.Down(cmd.Context(), p, store, record); err != nil {
		return err
	}
	cmd.Printf("Instance %s is down\n", name)
	return nil
}
