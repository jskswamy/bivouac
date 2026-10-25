package sshidentity

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh/knownhosts"
)

var machineName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)
var userName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
var endpointName = regexp.MustCompile(`^[A-Za-z0-9.:-]+$`)

func Alias(name string) (string, error) {
	if !machineName.MatchString(name) {
		return "", fmt.Errorf("invalid SSH machine name %q", name)
	}
	return "bivouac-" + name, nil
}

func quoteSSH(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("empty SSH config value")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("control character in SSH config value")
		}
	}
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\"", nil
}

func endpoint(host string) (string, string, error) {
	port := "22"
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		port = p
	}
	if !endpointName.MatchString(host) {
		return "", "", fmt.Errorf("invalid SSH endpoint %q", host)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", "", fmt.Errorf("invalid SSH port %q", port)
	}
	return host, port, nil
}

func identityPaths(c Connection) ([]string, error) {
	var paths []string
	seen := map[string]bool{}
	for _, id := range c.Keys {
		path := id.PrivatePath
		if id.Agent {
			path = id.PublicPath
		}
		if _, err := quoteSSH(path); err != nil {
			return nil, fmt.Errorf("selected IdentityFile: %w", err)
		}
		if !strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("selected IdentityFile path must be absolute: %q", path)
		}
		if !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("connection has no selected IdentityFile paths")
	}
	return paths, nil
}

func hostKeyAliasFor(c Connection) (string, error) {
	target := c.PublicIP
	if target == "" {
		target = c.PreferredHost
	}
	host, port, err := endpoint(target)
	if err != nil {
		return "", err
	}
	return knownhosts.Normalize(net.JoinHostPort(host, port)), nil
}

func renderProfile(c Connection) (string, error) {
	alias, err := Alias(c.Name)
	if err != nil {
		return "", err
	}
	if c.Alias != alias {
		return "", fmt.Errorf("recorded SSH alias %q does not match machine %q", c.Alias, c.Name)
	}
	if !userName.MatchString(c.User) {
		return "", fmt.Errorf("invalid SSH user %q", c.User)
	}
	paths, err := identityPaths(c)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if c.UseIAP {
		hostKeyAlias, err := hostKeyAliasFor(c)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Host %s\n  ProxyCommand bivouac proxy %s\n  User %s\n  HostKeyAlias %s\n  IdentitiesOnly yes\n", alias, c.Name, c.User, hostKeyAlias)
	} else {
		host, port, err := endpoint(c.PreferredHost)
		if err != nil {
			return "", err
		}
		hostKeyAlias, err := hostKeyAliasFor(c)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Host %s\n  HostName %s\n  Port %s\n  User %s\n  HostKeyAlias %s\n  IdentitiesOnly yes\n", alias, host, port, c.User, hostKeyAlias)
	}
	for _, path := range paths {
		quoted, err := quoteSSH(path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  IdentityFile %s\n", quoted)
	}
	return b.String(), nil
}

func TransportHost(ctx context.Context, host, user string) (string, error) {
	c, ok := FromContext(ctx)
	if !ok {
		return host, nil
	}
	if c.Alias == "" || user != c.User || (host != c.PreferredHost && host != c.PublicIP && host != c.Name && host != c.Alias) {
		return "", fmt.Errorf("selected SSH connection does not cover %s@%s", user, host)
	}
	return c.Alias, nil
}
