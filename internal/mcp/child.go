package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// ErrNoNode is returned when npx cannot be found.
var ErrNoNode = errors.New("需要 Node.js 20+（找不到 npx），安装后重试 / Node.js 20+ required: npx not found")

// NpxPath locates npx (npx.cmd on Windows).
func NpxPath() (string, error) {
	names := []string{"npx"}
	if runtime.GOOS == "windows" {
		names = []string{"npx.cmd", "npx"}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", ErrNoNode
}

// BackendCommand returns the MCP server command for a backend.
//
//	devtools          npx -y chrome-devtools-mcp@<ver> --browserUrl http://127.0.0.1:<port>
//	playwright        npx -y @playwright/mcp@<ver> --cdp-endpoint=http://127.0.0.1:<port>
//	playwright-server npx -y @playwright/mcp@<ver> --endpoint=ws://127.0.0.1:<port>/
func BackendCommand(backend string, localPort int, version string) (name string, args []string, err error) {
	if version == "" {
		version = "latest"
	}
	npx, err := NpxPath()
	if err != nil {
		return "", nil, err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	switch backend {
	case "", "devtools":
		return npx, []string{"-y", "chrome-devtools-mcp@" + version, "--browserUrl", url}, nil
	case "playwright":
		return npx, []string{"-y", "@playwright/mcp@" + version, "--cdp-endpoint=" + url}, nil
	case "playwright-server":
		return npx, []string{"-y", "@playwright/mcp@" + version, fmt.Sprintf("--endpoint=ws://127.0.0.1:%d/", localPort)}, nil
	}
	return "", nil, fmt.Errorf("未知后端 %q / unknown backend (devtools|playwright|safari)", backend)
}

// RunChild starts the MCP server process, relays this process's stdin/stdout
// to it through Proxy, and returns the child's exit code.
func RunChild(ctx context.Context, name string, args []string, instructions string, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = stderr
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 1, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("启动 %s 失败 / cannot start MCP server: %w", name, err)
	}
	go func() { _ = Proxy(os.Stdin, stdin, stdout, os.Stdout, instructions) }()
	err = cmd.Wait()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if code < 0 {
		code = 1
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return code, err
	}
	return code, nil
}
