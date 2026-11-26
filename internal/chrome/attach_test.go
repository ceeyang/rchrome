package chrome

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeChrome mimics Chrome with chrome://inspect remote debugging on: 404 for
// discovery endpoints, and a plain echo of the first line on the tokenised path.
func fakeChrome(t *testing.T) (port int, wsPath string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	wsPath = "/devtools/browser/0123abcd-test"
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil {
					return
				}
				if req.URL.Path != wsPath {
					io.WriteString(c, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
					return
				}
				io.WriteString(c, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
				// echo whatever follows, like a very dumb CDP socket
				io.Copy(c, br)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, wsPath
}

func writeActivePort(t *testing.T, dir string, port int, wsPath string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(strconv.Itoa(port)+"\n"+wsPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Last Version"), []byte("158.0.1.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func serveOnce(t *testing.T, h func(net.Conn)) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	go func() { h(b) }()
	return a
}

func TestReadAttach(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadAttach(dir); err == nil {
		t.Fatal("missing file must error")
	}
	port, ws := fakeChrome(t)
	writeActivePort(t, dir, port, ws)
	a, err := ReadAttach(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Port != port || a.WSPath != ws || a.Version != "158.0.1.2" || a.Browser() != "Chrome/158.0.1.2" {
		t.Fatalf("got %+v browser=%s", a, a.Browser())
	}
	// stale file: port nobody listens on
	writeActivePort(t, dir, 1, ws)
	if _, err := ReadAttach(dir); err == nil {
		t.Fatal("stale port must error")
	}
}

func TestAttachHandlerSynthesisesVersion(t *testing.T) {
	dir := t.TempDir()
	port, ws := fakeChrome(t)
	writeActivePort(t, dir, port, ws)
	c := serveOnce(t, AttachHandler(dir, t.Logf))
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, "GET /json/version HTTP/1.1\r\nHost: 127.0.0.1:19222\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var v Version
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	want := "ws://127.0.0.1:19222" + ws
	if v.WebSocket != want || v.Browser != "Chrome/158.0.1.2" {
		t.Fatalf("got %+v want ws %s", v, want)
	}
}

func TestAttachHandlerSplicesUpgrade(t *testing.T) {
	dir := t.TempDir()
	port, ws := fakeChrome(t)
	writeActivePort(t, dir, port, ws)
	c := serveOnce(t, AttachHandler(dir, t.Logf))
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, "GET "+ws+" HTTP/1.1\r\nHost: 127.0.0.1:19222\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: x\r\n\r\n")
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("status %q err %v", status, err)
	}
	for { // skip headers
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\r\n" {
			break
		}
	}
	io.WriteString(c, "hello-cdp\n")
	echo, err := br.ReadString('\n')
	if err != nil || echo != "hello-cdp\n" {
		t.Fatalf("echo %q err %v", echo, err)
	}
}

func TestAttachHandlerWithoutChrome(t *testing.T) {
	dir := t.TempDir()
	c := serveOnce(t, AttachHandler(dir, t.Logf))
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(c, "GET /json/version HTTP/1.1\r\nHost: 127.0.0.1:9222\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 503 || !strings.Contains(resp.Status, "chrome://inspect") {
		t.Fatalf("status %q", resp.Status)
	}
}

// TestAttachHandlerRealChrome talks to the Chrome actually running on this
// machine over loopback only (read-only Browser.getVersion). Opt-in.
func TestAttachHandlerRealChrome(t *testing.T) {
	if os.Getenv("RCHROME_REAL_CHROME") == "" {
		t.Skip("set RCHROME_REAL_CHROME=1 to run against the local Chrome")
	}
	a, err := FindAttach("")
	if err != nil {
		t.Skip(err)
	}
	c := serveOnce(t, AttachHandler(a.UserDataDir, t.Logf))
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	io.WriteString(c, "GET /json/version HTTP/1.1\r\nHost: 127.0.0.1:19222\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	var v Version
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	t.Logf("synthesised: %s %s", v.Browser, v.WebSocket)
	if !strings.HasPrefix(v.WebSocket, "ws://127.0.0.1:19222/devtools/browser/") {
		t.Fatalf("unexpected ws url %s", v.WebSocket)
	}
}
