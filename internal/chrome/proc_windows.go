//go:build windows

package chrome

import (
	"os/exec"
	"strconv"
)

func setProcAttr(cmd *exec.Cmd) {}

func killTree(cmd *exec.Cmd) {
	pid := cmd.Process.Pid
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	_ = cmd.Process.Kill()
}
