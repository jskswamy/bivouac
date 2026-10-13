package cmd

import "os"

func runningUnderAgent() bool {
	if os.Getenv("BIVOUAC_AGENT") != "" {
		return true
	}
	if os.Getenv("CLAUDECODE") == "1" {
		return true
	}
	if os.Getenv("PI_CODING_AGENT") == "true" {
		return true
	}
	return os.Getenv("AI_AGENT") != ""
}

func agentJSONHint() string {
	return "Tip: use 'bivouac manifest' or '--json' for machine-readable output."
}
