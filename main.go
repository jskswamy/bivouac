package main

import (
	"os"

	"github.com/jskswamy/cloudlab/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
