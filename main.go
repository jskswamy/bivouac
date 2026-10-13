package main

import (
	"os"

	"github.com/jskswamy/bivouac/cmd"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := cmd.Execute(cmd.BuildInfo{Version: version, Commit: commit, Date: date}); err != nil {
		os.Exit(1)
	}
}
