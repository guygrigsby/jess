package gate

import "strings"

// Dangerous is a conservative heuristic over a shell command string, ported in
// spirit from codebot's approval classifier. The daemon uses it inside its
// Approver to auto-prompt on risky bash even when a tool cannot mark itself
// Safe. Returns true when the command looks destructive.
func Dangerous(cmd string) bool {
	c := strings.ToLower(cmd)
	for _, sig := range []string{"rm -rf", "mkfs", "dd if=", ":(){", "shutdown", "reboot", "kill -9", "launchctl unload", "> /dev/"} {
		if strings.Contains(c, sig) {
			return true
		}
	}
	return false
}
