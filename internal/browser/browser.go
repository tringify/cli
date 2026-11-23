// Package browser opens a URL in the default browser.
package browser

import (
	"os"
	"os/exec"
	"runtime"
)

// Open starts the default browser without waiting for it. Set
// TRINGIFY_NO_BROWSER=1 to only print the URL (for example over SSH).
func Open(url string) error {
	if os.Getenv("TRINGIFY_NO_BROWSER") != "" {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
