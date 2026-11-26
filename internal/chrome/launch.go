package chrome

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// Version is the interesting part of /json/version.
type Version struct {
	Browser   string `json:"Browser"`
	UserAgent string `json:"User-Agent"`
	WebSocket string `json:"webSocketDebuggerUrl"`
}

// Instance is a browser process started by Launch.
type Instance struct {
	cmd *exec.Cmd
}

// Launch starts the browser with the remote-debugging port on a dedicated profile.
func Launch(exe string, port int, profile string, headless bool, logw io.Writer) (*Instance, error) {
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
	}
	if headless {
		args = append(args, "--headless=new")
	}
	args = append(args, "about:blank")
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logw
	cmd.Stderr = logw
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动浏览器失败 / launch failed: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return &Instance{cmd: cmd}, nil
}

// PID of the browser process.
func (i *Instance) PID() int {
	if i == nil || i.cmd == nil || i.cmd.Process == nil {
		return 0
	}
	return i.cmd.Process.Pid
}

// Kill terminates the browser and its children.
func (i *Instance) Kill() {
	if i == nil || i.cmd == nil || i.cmd.Process == nil {
		return
	}
	killTree(i.cmd)
}

// WaitReady polls /json/version until the debugging port answers.
func WaitReady(ctx context.Context, port int, timeout time.Duration) (*Version, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		v, err := FetchVersion(ctx, port)
		if err == nil {
			return v, nil
		}
		last = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("调试端口 %d 在 %s 内没有就绪 / debug port not ready: %w", port, timeout, last)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// FetchVersion reads /json/version from a local debugging port.
func FetchVersion(ctx context.Context, port int) (*Version, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeVersion(resp)
}

// VersionOverConn reads /json/version through an already-open connection
// (used across the tunnel). hostPort is what goes into the Host header; Chrome
// only accepts localhost or an IP literal there.
func VersionOverConn(conn net.Conn, hostPort string) (*Version, error) {
	req, err := http.NewRequest("GET", "http://"+hostPort+"/json/version", nil)
	if err != nil {
		return nil, err
	}
	req.Host = hostPort
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeVersion(resp)
}

func decodeVersion(resp *http.Response) (*Version, error) {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var v Version
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	if v.WebSocket == "" {
		return nil, fmt.Errorf("/json/version 缺少 webSocketDebuggerUrl / not a CDP endpoint")
	}
	return &v, nil
}

// OSFromUserAgent guesses the remote OS from the browser's user agent.
func OSFromUserAgent(ua string) string {
	switch {
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		return "macOS"
	case strings.Contains(ua, "CrOS"):
		return "ChromeOS"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	}
	return "unknown OS"
}
