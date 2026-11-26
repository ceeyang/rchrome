package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestInjectInstructions(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"x"},"instructions":"old"}}`)
	out, ok := InjectInstructions(line, "1", "NEW")
	if !ok {
		t.Fatal("expected rewrite")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	got := m["result"].(map[string]any)["instructions"].(string)
	if !strings.HasPrefix(got, "NEW\n\nold") {
		t.Fatalf("instructions = %q", got)
	}
	if _, ok := InjectInstructions(line, "2", "NEW"); ok {
		t.Fatal("wrong id must not rewrite")
	}
	if _, ok := InjectInstructions([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":1}}`), "1", "NEW"); ok {
		t.Fatal("error response must not rewrite")
	}
}

func TestProxyRewritesOnlyInitialize(t *testing.T) {
	// A fake MCP server that answers each request after it has read it, as a
	// real server would; this keeps the initialize response behind its request.
	toServerR, toServerW := io.Pipe()
	fromServerR, fromServerW := io.Pipe()
	go func() {
		defer fromServerW.Close()
		sc := bufio.NewScanner(toServerR)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				t.Errorf("bad request line: %v", err)
				return
			}
			var body string
			switch req.Method {
			case "initialize":
				body = `{"serverInfo":{"name":"s"}}`
			default:
				body = `{"tools":[]}`
			}
			_, _ = io.WriteString(fromServerW, `{"jsonrpc":"2.0","id":`+string(req.ID)+`,"result":`+body+"}\n")
		}
	}()
	clientIn := strings.NewReader(`{"jsonrpc":"2.0","id":"a","method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":"b","method":"tools/list"}` + "\n")
	var clientOut bytes.Buffer
	if err := Proxy(clientIn, toServerW, fromServerR, &clientOut, "REMOTE"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(clientOut.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), clientOut.String())
	}
	if !strings.Contains(lines[0], `"instructions":"REMOTE"`) || !strings.Contains(lines[0], `"id":"a"`) {
		t.Fatalf("initialize response not rewritten: %s", lines[0])
	}
	if strings.Contains(lines[1], "instructions") || !strings.Contains(lines[1], `"id":"b"`) {
		t.Fatalf("tools/list response must be untouched: %s", lines[1])
	}
}
