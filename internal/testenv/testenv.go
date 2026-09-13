// Package testenv gives a test an isolated view of the user's home
// directory.
//
// It exists because isolation used to be opt-in: cloudlab resolves paths out
// of $HOME (config, cache, state, ~/.ssh/known_hosts), and a test that forgot
// to redirect it wrote to the developer's real files. That is not
// hypothetical -- reconcile.Connect's trust-on-first-connect left 190
// [127.0.0.1]:<ephemeral port> entries in a real ~/.ssh/known_hosts, and once
// the OS recycled one of those ports the suite failed with a host key
// mismatch that had nothing to do with the code under test.
//
// Run makes isolation the default, so a test has to opt out rather than
// remember to opt in.
package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

// Env is the isolated environment Run builds for one test.
type Env struct {
	// Home is the temporary directory standing in for the user's home. Every
	// path cloudlab derives from $HOME -- and, because the XDG variables are
	// cleared, every XDG base directory too -- resolves inside it.
	Home string
}

// Path joins parts onto the isolated home.
func (e Env) Path(parts ...string) string {
	return filepath.Join(append([]string{e.Home}, parts...)...)
}

// Run calls fn with $HOME pointed at a fresh temporary directory and the
// XDG_* variables cleared, so everything cloudlab reads or writes under the
// user's home lands inside Env.Home.
//
// Clearing the XDG variables rather than pointing each at its own temp dir is
// deliberate: internal/xdg then takes its documented fallback into
// Env.Home/.config, .cache and .local/state, which keeps one directory to
// inspect and exercises the branch a real user without XDG_* set gets.
//
// Teardown is deferred, so it runs whether fn returns, fails, or calls
// t.Fatal -- t.Fatal is runtime.Goexit, which still unwinds defers. Note that
// the environment itself needs no unwinding: t.Setenv restores the previous
// value and t.TempDir removes the directory, both on failure too. The defer
// is there for fixtures that the testing package does not manage, which
// register through t.Cleanup inside fn.
//
// The sandbox lasts for the whole enclosing test, not just the lambda: Run
// redirects the environment with t.Setenv, which restores at the end of the
// test that called it. So code after Run returns is still sandboxed, and two
// Runs in one test nest rather than reset. Give a case that needs its own
// sandbox its own t.Run.
//
// A test using Run must not call t.Parallel: t.Setenv forbids it, because the
// process environment is shared.
func Run(t *testing.T, fn func(t *testing.T, env Env)) {
	t.Helper()
	env := isolate(t)
	defer teardown(t, env)
	fn(t, env)
}

// Group runs name as a subtest with its own isolated home.
//
// Go's subtests nest, so Groups nest, and each level gets a fresh sandbox
// plus whatever setup its own body does before descending -- the shape a
// describe/context block gives you, without a DSL:
//
//	testenv.Group(t, "a repository with beads", func(t *testing.T, env testenv.Env) {
//	        repo := seedRepo(t, env)
//	        testenv.Group(t, "and an external remote", func(t *testing.T, env testenv.Env) {
//	                ...
//	        })
//	})
//
// Note that the inner sandbox is a different directory from the outer one,
// so state a parent set up under its own home is not visible to a child.
// Pass what the child needs down through the closure, as repo is above.
func Group(t *testing.T, name string, fn func(t *testing.T, env Env)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Helper()
		Run(t, fn)
	})
}

// Isolate is Run's setup without the callback, for a TestMain or a test that
// needs the isolation but not the nesting.
func Isolate(t *testing.T) Env {
	t.Helper()
	return isolate(t)
}

// RunMain isolates every test in a package at once, for use from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testenv.RunMain(m)) }
//
// This is the form that actually removes the bug class. Run and Isolate still
// require someone to remember them per test, and "remember to isolate" is
// precisely what failed: internal/reconcile/ssh_test.go had 39 connecting
// tests and 10 of them redirected HOME. With RunMain a test has to work at it
// to escape.
//
// os.Setenv rather than t.Setenv, because TestMain has no *testing.T. The
// temporary home is removed before RunMain returns, so the caller's os.Exit
// -- which skips defers -- does not strand it.
func RunMain(m *testing.M) int {
	home, err := os.MkdirTemp("", "cloudlab-testenv-")
	if err != nil {
		panic("testenv: creating the isolated home: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(home) }()

	if err := os.Setenv("HOME", home); err != nil {
		panic("testenv: setting HOME: " + err.Error())
	}
	for _, v := range xdgVars {
		if err := os.Unsetenv(v); err != nil {
			panic("testenv: clearing " + v + ": " + err.Error())
		}
	}
	return m.Run()
}

// xdgVars are the base directories a test must not share, listed in one place
// so a base added to internal/xdg has to be considered here too.
//
// XDG_CACHE_HOME is deliberately absent. Config and state carry values a test
// can observe, so sharing them leaks between tests; the cache holds only
// regenerable data, and sharing it is what the cache is for. Clearing it
// costs real time -- it sends Pkl to a cold package cache on every run, which
// measured at roughly 5s on internal/reconcile alone -- and buys nothing,
// since no test asserts on cache contents it did not put there. A test that
// does care sets XDG_CACHE_HOME itself.
var xdgVars = []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME"}

func isolate(t *testing.T) Env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Cleared, not set: see Run's comment on why the fallback is the wanted
	// behaviour.
	for _, v := range xdgVars {
		t.Setenv(v, "")
	}
	return Env{Home: home}
}

// teardown reports a test that escaped its sandbox.
//
// The check is cheap and catches the failure this package was written for: a
// path resolved from something other than the isolated home -- a hardcoded
// os.UserHomeDir() that slipped past, or a tool invoked with the real HOME in
// its environment -- writing where it should not.
func teardown(t *testing.T, env Env) {
	t.Helper()
	if _, err := os.Stat(env.Home); err != nil {
		t.Errorf("the isolated home %s went missing during the test: %v", env.Home, err)
	}
}
