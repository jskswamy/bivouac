package lifecycle

import (
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/state"
)

func TestAttachRoutesWorkspaceAndTabsToSameServer(t *testing.T) {
	address := state.HerdrAddress{MachineID: "mid", Session: "default", ConfigRoot: "~/.config/herdr"}
	local, err := herdrCommand(address, "workspace", "get", "w1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(local, " ") != "--machine mid workspace get w1" {
		t.Fatalf("local route = %v", local)
	}
	remote, err := remoteHerdrCommand(address, "pane", "list", "--workspace", "w1")
	if err != nil {
		t.Fatal(err)
	}
	session, args, err := parseRemoteHerdrCmd(remote)
	if err != nil {
		t.Fatal(err)
	}
	if session != "default" || strings.Join(args, " ") != "pane list --workspace w1" {
		t.Fatalf("remote route = %q %v", session, args)
	}
}

func TestHerdrAddressRejectsImplicitOrUnsafeNamespace(t *testing.T) {
	for _, address := range []state.HerdrAddress{
		{},
		{MachineID: "mid", ConfigRoot: "root"},
		{MachineID: "mid", Session: "default"},
		{MachineID: "mid", Session: "../other", ConfigRoot: "root"},
	} {
		if _, err := herdrCommand(address, "workspace", "list"); err == nil {
			t.Fatalf("herdrCommand(%+v) succeeded", address)
		}
	}
}
