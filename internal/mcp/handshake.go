// Package mcp hosts the MCP child process and rewrites its handshake.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// Proxy relays newline-delimited JSON-RPC between the MCP client (clientIn /
// clientOut, normally stdin / stdout) and the MCP server (toServer /
// fromServer). The only thing it changes is the response to the client's
// "initialize" request, where it prepends instructions so the model knows
// these tools drive a remote browser. It returns when both directions finish.
func Proxy(clientIn io.Reader, toServer io.WriteCloser, fromServer io.Reader, clientOut io.Writer, instructions string) error {
	var initID atomic.Value // canonical JSON of the initialize request id
	initID.Store("")
	var wg sync.WaitGroup
	wg.Add(2)
	var errIn, errOut error
	go func() {
		defer wg.Done()
		defer toServer.Close()
		errIn = relay(clientIn, toServer, func(line []byte) []byte {
			if instructions == "" {
				return line
			}
			var m struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
			}
			if json.Unmarshal(line, &m) == nil && m.Method == "initialize" && len(m.ID) > 0 {
				initID.Store(canon(m.ID))
			}
			return line
		})
	}()
	go func() {
		defer wg.Done()
		errOut = relay(fromServer, clientOut, func(line []byte) []byte {
			want, _ := initID.Load().(string)
			if want == "" {
				return line
			}
			out, ok := InjectInstructions(line, want, instructions)
			if ok {
				initID.Store("")
				return out
			}
			return line
		})
	}()
	wg.Wait()
	return errors.Join(errIn, errOut)
}

// InjectInstructions rewrites an initialize response (matched by id) so that
// result.instructions starts with instructions. ok is false when line is not
// that response; the input is then returned unchanged.
func InjectInstructions(line []byte, wantID string, instructions string) (out []byte, ok bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return line, false
	}
	id, has := m["id"]
	if !has || canon(id) != wantID {
		return line, false
	}
	res, has := m["result"]
	if !has {
		return line, false
	}
	var result map[string]any
	if json.Unmarshal(res, &result) != nil {
		return line, false
	}
	if old, _ := result["instructions"].(string); old != "" {
		result["instructions"] = instructions + "\n\n" + old
	} else {
		result["instructions"] = instructions
	}
	nb, err := json.Marshal(result)
	if err != nil {
		return line, false
	}
	m["result"] = nb
	out, err = json.Marshal(m)
	if err != nil {
		return line, false
	}
	return out, true
}

func canon(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// relay copies lines from r to w, passing each (without its newline) through f.
func relay(r io.Reader, w io.Writer, f func([]byte) []byte) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			out := f(trimmed)
			if _, werr := w.Write(append(out, '\n')); werr != nil {
				return werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
