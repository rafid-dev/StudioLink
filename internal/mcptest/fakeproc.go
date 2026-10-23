// Package mcptest provides an in-memory stand-in for StudioMCP for tests.
package mcptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// FakeProc is an in-memory stand-in for StudioMCP. It answers initialize,
// echoes other requests, and has tools that exercise the bridge:
// "notify" sends a notification before its result, "announce" sends one
// after it (so no call is waiting), "slow" answers late, "crash" exits
// without answering.
type FakeProc struct {
	inR, outR, errR *io.PipeReader
	inW, outW, errW *io.PipeWriter
	done            chan struct{}
	once            sync.Once
	slow            time.Duration
}

// FakeTools are the tools a FakeProc lists. Calls to tools other than
// notify, announce, slow and crash are echoed.
var FakeTools = []string{"notify", "announce", "slow", "crash", "execute_luau", "list_roblox_studios"}

// NewFakeProc starts a fake that answers "slow" after the given delay.
func NewFakeProc(slow time.Duration) *FakeProc {
	p := &FakeProc{done: make(chan struct{}), slow: slow}
	p.inR, p.inW = io.Pipe()
	p.outR, p.outW = io.Pipe()
	p.errR, p.errW = io.Pipe()
	go p.serve()
	return p
}

func (p *FakeProc) Stdin() io.WriteCloser { return p.inW }
func (p *FakeProc) Stdout() io.Reader     { return p.outR }
func (p *FakeProc) Stderr() io.Reader     { return p.errR }
func (p *FakeProc) Done() <-chan struct{} { return p.done }
func (p *FakeProc) Kill() error {
	p.once.Do(func() {
		close(p.done)
		p.inR.Close()
		p.outW.Close()
		p.errW.Close()
	})
	return nil
}

func (p *FakeProc) write(v any) {
	b, _ := json.Marshal(v)
	_, _ = p.outW.Write(append(b, '\n'))
}

func (p *FakeProc) serve() {
	sc := bufio.NewScanner(p.inR)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			fmt.Fprintln(p.errW, "bad line:", sc.Text())
			continue
		}
		if len(m.ID) == 0 || m.Method == "" {
			continue // notification or response
		}
		reply := func(result any) {
			p.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		}
		switch {
		case m.Method == "initialize":
			reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]string{"name": "fake"}})
		case m.Method == "tools/list":
			var tools []map[string]string
			for _, name := range FakeTools {
				tools = append(tools, map[string]string{"name": name})
			}
			reply(map[string]any{"tools": tools})
		case m.Params.Name == "notify":
			p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]string{"data": "hi"}})
			reply("notified")
		case m.Params.Name == "announce":
			reply("announced")
			p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/resources/list_changed"})
		case m.Params.Name == "slow":
			go func() { time.Sleep(p.slow); reply("slow done") }()
		case m.Params.Name == "crash":
			p.Kill()
			return
		default:
			reply(map[string]string{"echo": m.Method})
		}
	}
}
