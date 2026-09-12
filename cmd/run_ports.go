package cmd

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
	"github.com/jskswamy/cloudlab/internal/state"
)

// errDiscoveryNoPort, errNoListeners and errAskUser are sentinels
// chooseListener returns when finishing the decision needs something it
// doesn't have: the discovery error itself and the instance name (both
// kept by the caller, which wraps them into the final message), or a
// terminal to prompt on. Named errors here are less code than widening
// the signature to a third return value every unambiguous caller would
// have to ignore.
var (
	errDiscoveryNoPort = errors.New("discovery failed, no port given")
	errNoListeners     = errors.New("no listeners")
	errAskUser         = errors.New("ask the user")
	errNoServed        = errors.New("nothing is being served")
)

// portVocabulary is a caller's own words for "give me a port" and "go
// find out what's available", so chooseListener's messages can speak
// connect's flag or serve's positional without knowing which one called
// it. connect and serve name a port two different ways -- a --port flag
// versus a bare positional -- and a message that assumes the former
// sends a serve user chasing a flag cobra will reject.
type portVocabulary struct {
	pick     string // e.g. "pass --port" or "name a port"
	recovery string // e.g. "run `cloudlab connect` with no --port to see what is"
}

var connectPortVocabulary = portVocabulary{
	pick:     "pass --port",
	recovery: "run `cloudlab connect` with no --port to see what is",
}

var servePortVocabulary = portVocabulary{
	pick:     "name a port",
	recovery: "run `cloudlab serve` with no port to see what is",
}

// chooseListener decides which listener runConnect (or runServe) targets,
// given what discovery found (or didn't). It is the pure half of that
// decision -- pulled out so the ladder itself is table-testable without a
// fake SSH server, the same reason sshArgs/tmuxArgs/herdrArgs/pairArgs
// exist as pure functions in internal/lifecycle. The interactive prompt is
// not pure (it reads a terminal), so ambiguity with more than one candidate
// comes back as errAskUser for the caller to act on, rather than being
// resolved here.
func chooseListener(listeners []lifecycle.Listener, port int, discoveryFailed, interactive bool, vocab portVocabulary) (lifecycle.Listener, error) {
	switch {
	case discoveryFailed && port == 0:
		return lifecycle.Listener{}, errDiscoveryNoPort
	case discoveryFailed:
		// No listing to check against, so the bind address is unknown.
		// Forward rather than guess: a forward reaches a service on any
		// address, while a tailnet URL reaches only a routable one.
		return lifecycle.Listener{Addr: "127.0.0.1", Port: port}, nil
	case port != 0:
		for _, l := range listeners {
			if l.Port == port {
				return l, nil
			}
		}
		return lifecycle.Listener{}, fmt.Errorf("nothing is listening on port %d — %s", port, vocab.recovery)
	case len(listeners) == 0:
		return lifecycle.Listener{}, errNoListeners
	case len(listeners) == 1:
		// One candidate is not a choice -- neither a prompt nor a
		// refusal is warranted when there is nothing to pick between.
		return listeners[0], nil
	case !interactive:
		return lifecycle.Listener{}, fmt.Errorf("several ports are listening; %s (no terminal to ask on)", vocab.pick)
	default:
		return lifecycle.Listener{}, errAskUser
	}
}

// chooseServeEntry is chooseListener's sibling for ports that are already
// published, but not the same shape: it has no discovery-failure branches
// (unserve always lists what tailscale has on file, nothing to fall back
// blind on), and the empty check runs FIRST rather than after the
// port-match check. That order is deliberate -- `unserve 8888` against an
// instance serving nothing should get the friendly "nothing is being
// served" message from runUnserve's errNoServed branch, not a bare "port
// 8888 is not being served" that implies something else is.
func chooseServeEntry(entries []lifecycle.ServeEntry, port int, interactive bool) (lifecycle.ServeEntry, error) {
	switch {
	case len(entries) == 0:
		return lifecycle.ServeEntry{}, errNoServed
	case port != 0:
		for _, e := range entries {
			if e.Port == port {
				return e, nil
			}
		}
		return lifecycle.ServeEntry{}, fmt.Errorf("port %d is not being served — run `cloudlab unserve` with no port to see what is", port)
	case len(entries) == 1:
		return entries[0], nil
	case !interactive:
		return lifecycle.ServeEntry{}, fmt.Errorf("several ports are being served; name one (no terminal to ask on)")
	default:
		return lifecycle.ServeEntry{}, errAskUser
	}
}

// discoverAndChoose runs the shared listener discovery both connect and
// serve need: list what is listening, hide the machine's own sockets
// unless asked, and settle on one candidate. It also hands back the raw
// offered/listeners slices, since both callers report afterward when a
// sole candidate was auto-selected and how many sockets a filter hid.
//
// tolerateDiscoveryFailure is what separates the two callers. connect can
// still forward to a port the user named without a listing, since a
// forward reaches any bind address; serve cannot, because the bind address
// is exactly what decides whether an entry is needed at all.
//
// vocab supplies chooseListener's caller-specific wording -- connect names
// a port with --port, serve with a bare positional -- so the same ladder
// can tell either caller's user how to retry without borrowing the other
// command's syntax.
func discoverAndChoose(cmd *cobra.Command, record state.Record, name string, port int, all, tolerateDiscoveryFailure bool, vocab portVocabulary) (chosen lifecycle.Listener, offered, listeners []lifecycle.Listener, err error) {
	// Discovery is best-effort when tolerated. `ss` comes from iproute2 and
	// is present on every image cloudlab boots, but an unusual base image
	// or a locked-down PATH should not make connect unusable when the
	// caller already knows the port. serve cannot make the same trade --
	// the bind address is exactly what decides whether an entry is needed
	// at all -- so a failed listing is fatal there instead.
	listeners, lerr := lifecycle.Listeners(cmd.Context(), record.IP, record.User)
	if lerr != nil && !tolerateDiscoveryFailure {
		return lifecycle.Listener{}, nil, nil, lerr
	}

	// A named port addresses a socket directly, so it searches everything:
	// a user who names one has said what they want, and hiding it would
	// only produce a puzzling "nothing is listening" for a port they can
	// see is open. The filter narrows what gets *offered*, not what can
	// be reached.
	offered = lifecycle.VisibleListeners(listeners, all || port != 0)

	chosen, err = chooseListener(offered, port, tolerateDiscoveryFailure && lerr != nil, isInteractive(), vocab)
	switch {
	case errors.Is(err, errDiscoveryNoPort):
		return lifecycle.Listener{}, offered, listeners, fmt.Errorf("%w\npass --port to connect without discovery", lerr)
	case errors.Is(err, errNoListeners):
		// Say so when the filter is why nothing is on offer, rather than
		// claiming an instance running seven sockets is running none.
		if hidden := len(listeners) - len(offered); hidden > 0 {
			return lifecycle.Listener{}, offered, listeners, fmt.Errorf("nothing of yours is listening on %s (%d infrastructure socket(s) hidden — pass --all to see them)", name, hidden)
		}
		return lifecycle.Listener{}, offered, listeners, fmt.Errorf("nothing is listening on %s", name)
	case errors.Is(err, errAskUser):
		if chosen, err = pickListener(cmd, offered); err != nil {
			return lifecycle.Listener{}, offered, listeners, err
		}
	case err != nil:
		return lifecycle.Listener{}, offered, listeners, err
	}
	return chosen, offered, listeners, nil
}

// runConnect reaches a service on the instance. With a port it goes
// straight there; without one it asks the instance what is listening.
func runConnect(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}

	// args[0] is the instance name (named: true), so a port comes from
	// the flag rather than a positional -- same reason runSSH takes
	// --dir rather than a second positional.
	port, err := cmd.Flags().GetInt("port")
	if err != nil {
		return err
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}

	chosen, offered, listeners, err := discoverAndChoose(cmd, record, name, port, all, true, connectPortVocabulary)
	if err != nil {
		return err
	}

	// Say what was picked when the user did not pick it. A sole
	// candidate is auto-selected rather than prompted for, and the
	// filter is usually why there is only one -- without this line the
	// menu simply vanishes and the command starts reaching something
	// the user never named.
	if port == 0 && len(offered) == 1 {
		what := chosen.Process
		if what == "" {
			what = "unknown process"
		}
		cmd.Printf("Only one service is listening: %d (%s)\n", chosen.Port, what)
		if hidden := len(listeners) - len(offered); hidden > 0 {
			cmd.Printf("  %d infrastructure socket(s) hidden — pass --all to see them\n", hidden)
		}
	}

	// Skip the round trip entirely when state already says there is no
	// tailnet -- an extra SSH connect plus `tailscale ip` to relearn
	// what record.TailscaleJoined already told us. Mirrors the same
	// guard on choosePairHost below.
	var tailnetIP string
	if record.TailscaleJoined {
		tailnetIP, err = lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
		if err != nil {
			// Not reachable over the tailnet is an expected state, not a
			// failure -- ConnectTarget treats an empty tailnetIP as "forward
			// instead of routing there directly".
			tailnetIP = ""
		}
	}

	// Routable: nothing to set up, so nothing can fail after this line.
	// chosen.Port stands in for the local port here only to satisfy the
	// signature -- this branch never forwards, so it goes unused.
	routableURL, mustForward := lifecycle.ConnectTarget(tailnetIP, chosen, chosen.Port)
	if !mustForward {
		cmd.Println(routableURL)
		return nil
	}

	// The near side is settled BEFORE anything is printed. Otherwise the
	// "forwarding over SSH" line goes out first and a failed bind
	// contradicts it two lines later, which is the success-shaped-output
	// problem this command has already been bitten by once.
	wanted, err := cmd.Flags().GetInt("local-port")
	if err != nil {
		return err
	}
	mustUse := wanted != 0
	if !mustUse {
		wanted = chosen.Port
	}
	localPort, err := lifecycle.FreeLocalPort(wanted, mustUse)
	if err != nil {
		if errors.Is(err, lifecycle.ErrLocalPortBusy) {
			return fmt.Errorf("%w — something already holds it locally; pass a different --local-port, or omit the flag to let cloudlab pick a free one", err)
		}
		return err
	}
	// Only when cloudlab picked the number. Saying "is taken, forwarding
	// through N instead" to someone who asked for N reads as though the
	// request was overridden.
	if !mustUse && localPort != chosen.Port {
		cmd.Printf("Local port %d is taken, forwarding through %d instead\n", chosen.Port, localPort)
	}

	url, _ := lifecycle.ConnectTarget(tailnetIP, chosen, localPort)
	cmd.Printf("%s (forwarding over SSH — Ctrl-C to stop)\n", url)
	// Over the tailnet when there is one. Reaching this line means
	// TailscaleIP already answered, so falling back to the public IP
	// here would route around a link just proven to be up.
	host := record.IP
	if tailnetIP != "" {
		host = tailnetIP
	}
	return lifecycle.Forward(cmd.Context(), host, record.User, localPort, chosen.Port)
}

// serveTarget returns the tailnet URL a listener will answer at, and
// whether publishing it requires a serve entry at all.
//
// Split out as a pure function for the same reason ConnectTarget was:
// the routable-versus-loopback rule is the part worth testing, and it
// needs no network to test.
func serveTarget(tailnetIP string, l lifecycle.Listener) (string, bool) {
	scheme := ""
	if l.HTTP() {
		scheme = "http://"
	}
	return scheme + tailnetIP + ":" + strconv.Itoa(l.Port), l.LoopbackOnly()
}

// runServe publishes a service on the tailnet, where it outlives this
// command. Discovery is identical to connect's: the two commands differ
// in how long the result lasts, not in how you name what you want.
func runServe(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	// Checked before any SSH work: serving is meaningless without a
	// tailnet, and this says so instead of surfacing a tailscale error.
	if !record.TailscaleJoined {
		return fmt.Errorf("%s is not on a tailnet — serving publishes there, so run `cloudlab tailscale` first", name)
	}

	port := 0
	if len(args) > 0 {
		if port, err = strconv.Atoi(args[0]); err != nil {
			return fmt.Errorf("%q is not a port number", args[0])
		}
	}
	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}

	chosen, offered, listeners, err := discoverAndChoose(cmd, record, name, port, all, false, servePortVocabulary)
	if err != nil {
		return err
	}

	// Say what was picked when the user did not pick it -- the same notice
	// runConnect prints, and more warranted here: a forward dies with the
	// command, but this publishes on the tailnet until `unserve` undoes it,
	// so silently picking the wrong service is a mistake that outlives the
	// command that made it.
	if port == 0 && len(offered) == 1 {
		what := chosen.Process
		if what == "" {
			what = "unknown process"
		}
		cmd.Printf("Only one service is listening: %d (%s)\n", chosen.Port, what)
		if hidden := len(listeners) - len(offered); hidden > 0 {
			cmd.Printf("  %d infrastructure socket(s) hidden — pass --all to see them\n", hidden)
		}
	}

	tailnetIP, err := lifecycle.TailscaleIP(cmd.Context(), record.IP, record.User)
	if err != nil || tailnetIP == "" {
		return fmt.Errorf("could not resolve %s's tailnet address — is tailscaled running there?", name)
	}

	url, needsEntry := serveTarget(tailnetIP, chosen)
	if !needsEntry {
		cmd.Printf("%s is already reachable on the tailnet, no serving needed\n", url)
		return nil
	}
	if err := lifecycle.Serve(cmd.Context(), record.IP, record.User, chosen.Port); err != nil {
		return err
	}
	cmd.Printf("Serving %s:%d on your tailnet\n", chosen.Addr, chosen.Port)
	cmd.Printf("  %s\n", url)
	cmd.Printf("Stop with: cloudlab unserve %d\n", chosen.Port)
	return nil
}

// runUnserve stops publishing a port. It can only stop SERVED entries —
// a running `connect` forward is a foreground process in another
// terminal with no PID recorded, so the empty-state message says so
// rather than leaving someone to wonder why unserve did nothing.
func runUnserve(cmd *cobra.Command, name string, args []string) error {
	_, record, err := resolveInstance(name)
	if err != nil {
		return err
	}
	// Same guard as runServe/runStatus: serving is tailnet-only, so an
	// instance that never joined has nothing to unserve and should not
	// pay a full SSH connect plus a `command -v tailscale` probe just to
	// be told that.
	if !record.TailscaleJoined {
		return fmt.Errorf("%s is not on a tailnet — nothing is being served there", name)
	}

	port := 0
	if len(args) > 0 {
		if port, err = strconv.Atoi(args[0]); err != nil {
			return fmt.Errorf("%q is not a port number", args[0])
		}
	}

	entries, err := lifecycle.ServeStatus(cmd.Context(), record.IP, record.User)
	if err != nil {
		return err
	}

	chosen, err := chooseServeEntry(entries, port, isInteractive())
	switch {
	case errors.Is(err, errNoServed):
		return fmt.Errorf("nothing is being served on %s — a running `cloudlab connect` forward stops with Ctrl-C in its own terminal", name)
	case errors.Is(err, errAskUser):
		if chosen, err = pickServeEntry(cmd, entries); err != nil {
			return err
		}
	case err != nil:
		return err
	}

	if err := lifecycle.Unserve(cmd.Context(), record.IP, record.User, chosen.Port); err != nil {
		return err
	}
	cmd.Printf("Stopped serving %d\n", chosen.Port)
	return nil
}
