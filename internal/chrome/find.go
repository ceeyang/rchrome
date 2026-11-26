// Package chrome finds, launches and health-checks a Chromium-family browser.
package chrome

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ErrNotFound means no supported browser binary was found.
var ErrNotFound = errors.New("找不到 Chrome / Edge，请用 --chrome <路径> 指定 / Chrome not found; pass --chrome <path>")

// Find returns the first browser binary that exists, in preference order.
func Find() (string, error) {
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, app := range []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Chromium.app/Contents/MacOS/Chromium",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
		} {
			cands = append(cands, filepath.Join("/Applications", app))
			if home != "" {
				cands = append(cands, filepath.Join(home, "Applications", app))
			}
		}
	case "windows":
		for _, rel := range []string{`Google\Chrome\Application\chrome.exe`, `Microsoft\Edge\Application\msedge.exe`} {
			for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
				if base := os.Getenv(env); base != "" {
					cands = append(cands, filepath.Join(base, rel))
				}
			}
		}
	default:
		for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if p, err := exec.LookPath(n); err == nil {
				return p, nil
			}
		}
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	if runtime.GOOS == "windows" {
		for _, n := range []string{"chrome.exe", "msedge.exe"} {
			if p, err := exec.LookPath(n); err == nil {
				return p, nil
			}
		}
	}
	return "", ErrNotFound
}
