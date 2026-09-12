package wizard

import "runtime"

// runtimeCaller locates this package's own directory, so the tests can
// read Config.pkl without depending on the working directory.
func runtimeCaller() (uintptr, string, int, bool) {
	return runtime.Caller(0)
}
