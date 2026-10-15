package lifecycle

import (
	"fmt"
	"path"
	"strings"

	"github.com/jskswamy/bivouac/internal/shellcmd"
	"github.com/jskswamy/bivouac/internal/state"
)

// herdrCommand addresses the saved profile whose server issued all handles in
// args. ConfigRoot and Session remain part of the durable identity even though
// Herdr's --machine forwarding reads them from the profile rather than argv.
func herdrCommand(address state.HerdrAddress, args ...string) ([]string, error) {
	if err := validateHerdrAddress(address); err != nil {
		return nil, err
	}
	return append([]string{"--machine", address.MachineID}, args...), nil
}

// remoteHerdrCommand addresses the same server namespace through one existing
// SSH connection, avoiding --machine's connection cost for tab layout.
func remoteHerdrCommand(address state.HerdrAddress, args ...string) (string, error) {
	if err := validateHerdrAddress(address); err != nil {
		return "", err
	}
	inner := "herdr --session " + shellcmd.Quote(address.Session)
	for _, arg := range args {
		inner += " " + shellcmd.Quote(arg)
	}
	return shellcmd.LoginShell(inner), nil
}

func legacyHerdrAddress(machine string) state.HerdrAddress {
	return state.HerdrAddress{MachineID: machine, Session: "default", ConfigRoot: "~/.config/herdr"}
}

func coerceHerdrAddress(value any) (state.HerdrAddress, error) {
	switch address := value.(type) {
	case state.HerdrAddress:
		return address, validateHerdrAddress(address)
	case string:
		converted := legacyHerdrAddress(address)
		return converted, validateHerdrAddress(converted)
	default:
		return state.HerdrAddress{}, fmt.Errorf("unsupported herdr address %T", value)
	}
}

func validateHerdrAddress(address state.HerdrAddress) error {
	if address.MachineID == "" {
		return fmt.Errorf("herdr address has no saved machine id")
	}
	if err := CheckSessionName(address.Session); err != nil {
		return fmt.Errorf("herdr address has invalid session %q: %w", address.Session, err)
	}
	if address.ConfigRoot == "" || strings.ContainsRune(address.ConfigRoot, '\x00') || path.Clean(address.ConfigRoot) != address.ConfigRoot {
		return fmt.Errorf("herdr address has invalid config root %q", address.ConfigRoot)
	}
	return nil
}
