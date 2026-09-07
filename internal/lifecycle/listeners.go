package lifecycle

import (
	"regexp"
	"strconv"
	"strings"
)

// Listener is one listening socket reported by `ss -tlnp`.
//
// Addr is kept verbatim rather than normalised because it is the whole
// point: a service on 127.0.0.1 is not reachable at the instance's
// tailnet address, and connect has to be able to tell.
type Listener struct {
	Addr    string
	Port    int
	Process string
}

// loopback matches the addresses that are reachable only from the
// instance itself. 127.0.0.0/8 is matched by prefix rather than parsed:
// ss writes interface-scoped forms like "127.0.0.53%lo" that net.ParseIP
// rejects outright.
func (l Listener) LoopbackOnly() bool {
	return strings.HasPrefix(l.Addr, "127.") || l.Addr == "::1"
}

// infraPorts are the ports connect hides by default. Both belong to
// services cloud-init and the template put there, never to work someone
// started on the instance: 22 is sshd, which `cloudlab ssh` already
// reaches, and 53 is systemd-resolved, which is not addressable from
// off-box at all.
var infraPorts = map[int]bool{22: true, 53: true}

// Infrastructure reports whether this socket belongs to the machine
// rather than to the user's work.
//
// A real instance listens on seven sockets of which one is typically the
// user's: sshd on v4 and v6, systemd-resolved twice, tailscaled twice,
// and the one service they actually started. Offering all seven makes
// the useful entry the exception.
//
// Tailscale's own sockets are recognised by their bind address rather
// than a port, because tailscaled picks an ephemeral one -- 100.64.0.0/10
// is the CGNAT range Tailscale assigns, and fd7a:115c:a1e0::/48 its IPv6
// equivalent. A service the user bound to 0.0.0.0 still shows up even
// though it is reachable at the same tailnet address; only sockets bound
// exclusively to it are tailscaled's.
func (l Listener) Infrastructure() bool {
	if infraPorts[l.Port] {
		return true
	}
	return strings.HasPrefix(l.Addr, "100.") || strings.HasPrefix(l.Addr, "fd7a:115c:a1e0")
}

// HTTP reports whether http:// is a defensible scheme for this port.
//
// connect prints a URL, and printing http://host:22 for sshd invites
// exactly the wrong client -- curl against it returns "Received HTTP/0.9
// when not allowed", which is sshd's version banner being read as a
// response. Anything not known to speak something else is assumed to be
// HTTP: connect exists for web services, and a wrong guess on an
// unusual port costs a scheme the user can retype.
func (l Listener) HTTP() bool {
	return !infraPorts[l.Port]
}

// processName pulls the program name out of ss's process column,
// users:(("moshi-hook",pid=10954,fd=7)). Absent for sockets owned by
// another user, which is every root-owned service when ss runs
// unprivileged -- so an empty name is normal, not an error.
var processName = regexp.MustCompile(`users:\(\("([^"]+)"`)

// parseListeners turns `ss -tlnp` output into Listeners, skipping the
// header and any line that is not a LISTEN row.
func parseListeners(out string) []Listener {
	var listeners []Listener
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		// fields[3] is "Local Address:Port". The address may itself
		// contain colons (IPv6), so the port is after the LAST one.
		local := fields[3]
		i := strings.LastIndex(local, ":")
		if i < 0 {
			continue
		}
		port, err := strconv.Atoi(local[i+1:])
		if err != nil {
			continue
		}
		addr := strings.Trim(local[:i], "[]")

		var proc string
		if m := processName.FindStringSubmatch(line); m != nil {
			proc = m[1]
		}
		listeners = append(listeners, Listener{Addr: addr, Port: port, Process: proc})
	}
	return listeners
}
