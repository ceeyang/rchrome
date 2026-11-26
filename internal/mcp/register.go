package mcp

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// ClaudePath locates the Claude Code CLI.
func ClaudePath() (string, error) {
	names := []string{"claude"}
	if runtime.GOOS == "windows" {
		names = []string{"claude.cmd", "claude.exe", "claude"}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("找不到 claude 命令 / Claude Code CLI not found in PATH")
}

// ClaudeRegister (re)registers an MCP server with Claude Code.
func ClaudeRegister(scope, name, command string, args []string) error {
	cl, err := ClaudePath()
	if err != nil {
		return err
	}
	_ = exec.Command(cl, "mcp", "remove", "-s", scope, name).Run()
	add := append([]string{"mcp", "add", "-s", scope, name, "--", command}, args...)
	out, err := exec.Command(cl, add...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude mcp add 失败 / failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ManualJSON renders the mcpServers snippet for clients other than Claude Code.
func ManualJSON(name, command string, args []string) string {
	doc := map[string]any{
		"mcpServers": map[string]any{
			name: map[string]any{"command": command, "args": args},
		},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b)
}
