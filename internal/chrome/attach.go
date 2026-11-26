package chrome

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Attach describes a running browser that has remote debugging enabled via
// chrome://inspect/#remote-debugging (Chrome 144+). Chrome writes the port and a
// tokenised WebSocket path into <user-data-dir>/DevToolsActivePort; discovery
// endpoints such as /json/version answer 404 in that mode, so rchrome
// synthesises them (see AttachHandler).
type Attach struct {
	UserDataDir string
	Port        int
	WSPath      string // e.g. /devtools/browser/<uuid>
	Version     string // from <user-data-dir>/Last Version, may be empty
}

// ErrNotEnabled explains how to turn the feature on.
var ErrNotEnabled = errors.New("远程 Chrome 没有开启远程调试：在那台机器的 Chrome 里打开 chrome://inspect/#remote-debugging，勾选「Allow remote debugging for this browser instance」（Chrome 144+），保持 Chrome 运行后重试 / remote debugging not enabled in the running Chrome")

// DefaultUserDataDirs lists the standard profile roots, most likely first.
func DefaultUserDataDirs() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		return []string{
			filepath.Join(base, "Google", "Chrome"),
			filepath.Join(base, "Microsoft Edge"),
			filepath.Join(base, "Chromium"),
			filepath.Join(base, "BraveSoftware", "Brave-Browser"),
		}
	case "windows":
		local := os.Getenv("LocalAppData")
		return []string{
			filepath.Join(local, "Google", "Chrome", "User Data"),
			filepath.Join(local, "Microsoft", "Edge", "User Data"),
			filepath.Join(local, "Chromium", "User Data"),
			filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data"),
		}
	default:
		cfg := filepath.Join(home, ".config")
		return []string{
			filepath.Join(cfg, "google-chrome"),
			filepath.Join(cfg, "microsoft-edge"),
			filepath.Join(cfg, "chromium"),
			filepath.Join(cfg, "BraveSoftware", "Brave-Browser"),
		}
	}
}

// FindAttach locates a running browser with remote debugging enabled. dir, if
// non-empty, is the only user-data-dir considered.
func FindAttach(dir string) (*Attach, error) {
	dirs := []string{dir}
	if dir == "" {
		dirs = DefaultUserDataDirs()
	}
	var lastErr error
	for _, d := range dirs {
		a, err := ReadAttach(d)
		if err == nil {
			return a, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrNotEnabled
}

// ReadAttach parses DevToolsActivePort in dir and checks the port is listening.
func ReadAttach(dir string) (*Attach, error) {
	b, err := os.ReadFile(filepath.Join(dir, "DevToolsActivePort"))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("%s/DevToolsActivePort 格式不对 / malformed", dir)
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || port <= 0 {
		return nil, fmt.Errorf("%s/DevToolsActivePort 端口不合法 / bad port", dir)
	}
	wsPath := strings.TrimSpace(lines[1])
	if !strings.HasPrefix(wsPath, "/") {
		return nil, fmt.Errorf("%s/DevToolsActivePort ws 路径不合法 / bad ws path", dir)
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		// Stale file from a Chrome that has since quit.
		return nil, fmt.Errorf("%w（%s 里记录的端口 %d 没有在监听，Chrome 是否已退出？）", ErrNotEnabled, dir, port)
	}
	_ = c.Close()
	a := &Attach{UserDataDir: dir, Port: port, WSPath: wsPath}
	if v, err := os.ReadFile(filepath.Join(dir, "Last Version")); err == nil {
		a.Version = strings.TrimSpace(string(v))
	}
	return a, nil
}

// Browser returns a /json/version style product string.
func (a *Attach) Browser() string {
	name := "Chrome"
	switch {
	case strings.Contains(a.UserDataDir, "Edge"):
		name = "Edg"
	case strings.Contains(a.UserDataDir, "Brave"):
		name = "Brave"
	case strings.Contains(a.UserDataDir, "hromium"):
		name = "Chromium"
	}
	if a.Version == "" {
		return name
	}
	return name + "/" + a.Version
}

// AttachHandler serves one tunnel connection against the attached browser.
// GET /json/version (and /json, /json/list) are answered locally, pointing the
// client at the tokenised WebSocket path; everything else, i.e. the WebSocket
// upgrade itself, is spliced byte-for-byte to the real debugging port. The
// DevToolsActivePort file is re-read per request so a restarted Chrome keeps
// working without restarting rchrome.
func AttachHandler(dir string, logf func(string, ...any)) func(net.Conn) {
	return func(c net.Conn) {
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		br := bufio.NewReaderSize(c, 64<<10)
		raw, err := readHeaderBlock(br)
		if err != nil {
			return
		}
		_ = c.SetReadDeadline(time.Time{})
		reqLine := raw
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			reqLine = raw[:i]
		}
		fields := strings.Fields(string(reqLine))
		method, path := "", ""
		if len(fields) >= 2 {
			method, path = fields[0], fields[1]
		}
		a, err := ReadAttach(dir)
		if err != nil {
			msg := err.Error()
			fmt.Fprintf(c, "HTTP/1.1 503 remote Chrome has no remote debugging enabled - see chrome://inspect/#remote-debugging\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
			return
		}
		if method == "GET" && (path == "/json/version" || path == "/json/version/" || path == "/json" || path == "/json/list") {
			host := headerValue(raw, "Host")
			if host == "" {
				host = "127.0.0.1:9222"
			}
			body := fmt.Sprintf(`{"Browser":%q,"Protocol-Version":"1.3","User-Agent":"","webSocketDebuggerUrl":"ws://%s%s","rchrome":"attached to %s"}`,
				a.Browser(), host, a.WSPath, a.UserDataDir)
			if path == "/json" || path == "/json/list" {
				body = "[]"
			}
			fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/json; charset=UTF-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
			return
		}
		up, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", a.Port), 5*time.Second)
		if err != nil {
			logf("attach: dial %d: %v", a.Port, err)
			return
		}
		defer up.Close()
		if _, err := up.Write(raw); err != nil {
			return
		}
		if n := br.Buffered(); n > 0 {
			extra, _ := br.Peek(n)
			if _, err := up.Write(extra); err != nil {
				return
			}
			_, _ = br.Discard(n)
		}
		splice(c, up)
	}
}

// readHeaderBlock returns the raw bytes up to and including the blank line.
func readHeaderBlock(br *bufio.Reader) ([]byte, error) {
	var raw []byte
	for {
		line, err := br.ReadBytes('\n')
		raw = append(raw, line...)
		if err != nil {
			return raw, err
		}
		if len(raw) > 64<<10 {
			return raw, errors.New("header too large")
		}
		if bytes.Equal(bytes.TrimRight(line, "\r\n"), nil) {
			return raw, nil
		}
	}
}

func headerValue(raw []byte, name string) string {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		k, v, ok := strings.Cut(string(bytes.TrimRight(line, "\r")), ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type closeWriter interface{ CloseWrite() error }

func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}
