package testenv

import (
	"fmt"
	"strings"
	"sync"
)

// AsSystemd makes handler's view of the instance include systemd: the switch
// is started with systemd-run and followed through systemctl and journalctl,
// so a fake that only ever sees the switch command would answer the follow
// loop with silence and leave it polling. The wrapped handler still sees
// the switch command itself, and everything that is not systemd.
func AsSystemd(handler func(cmd string, stdin []byte) (string, uint32)) func(string, []byte) (string, uint32) {
	var mu sync.Mutex
	started, drained := false, false
	var out string
	var code uint32
	return func(cmd string, stdin []byte) (string, uint32) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(cmd, "systemd-run"):
			started = true
			out, code = handler(cmd, stdin)
			return "", 0
		case strings.Contains(cmd, "systemctl show"):
			switch {
			case !started:
				return "LoadState=loaded\nSubState=dead\n", 0
			case code != 0:
				return fmt.Sprintf("InvocationID=inv\nLoadState=loaded\nSubState=failed\nResult=exit-code\nExecMainStatus=%d\n", code), 0
			}
			return "InvocationID=inv\nLoadState=loaded\nSubState=exited\nResult=success\nExecMainStatus=0\n", 0
		case strings.Contains(cmd, "journalctl"):
			if drained {
				return "-- cursor: end\n", 0
			}
			drained = true
			return out + "-- cursor: end\n", 0
		case strings.Contains(cmd, "systemctl stop"):
			return "", 0
		}
		return handler(cmd, stdin)
	}
}
