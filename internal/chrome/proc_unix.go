//go:build !windows

package chrome

import (
	"os/exec"
	"syscall"
	"time"
)

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killTree(cmd *exec.Cmd) {
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 30; i++ {
			if err := syscall.Kill(-pid, 0); err != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
