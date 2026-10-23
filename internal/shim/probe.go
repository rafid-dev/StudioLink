package shim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ProbeResult describes a working connection to the bridge.
type ProbeResult struct {
	Server   string // StudioMCP's server name and version
	Protocol string // MCP protocol version agreed
	Tools    int
}

// Probe connects through the shim exactly as an agent would: it initializes
// a session, lists the tools, then ends the session.
func Probe(ctx context.Context, opts Options) (*ProbeResult, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ran := make(chan error, 1)
	go func() {
		ran <- Run(ctx, opts, inR, outW)
		outW.Close()
	}()
	defer func() {
		inW.Close()
		go io.Copy(io.Discard, outR) // let Run finish writing anything left
		<-ran
	}()

	dec := json.NewDecoder(outR)
	call := func(id int, method string, params any, result any) error {
		if err := send(inW, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return err
		}
		for {
			var resp struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := dec.Decode(&resp); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("%s: no reply: %w", method, err)
			}
			switch {
			case resp.ID == nil:
				continue // a notification from the server
			case resp.Error != nil:
				return fmt.Errorf("%s: %s", method, resp.Error.Message)
			case *resp.ID != id:
				continue
			}
			return json.Unmarshal(resp.Result, result)
		}
	}

	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	err := call(1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "studiolink-client check", "version": "1"},
	}, &init)
	if err != nil {
		return nil, err
	}
	if err := send(inW, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return nil, err
	}
	var tools struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := call(2, "tools/list", map[string]any{}, &tools); err != nil {
		return nil, err
	}
	return &ProbeResult{
		Server:   strings.TrimSpace(init.ServerInfo.Name + " " + init.ServerInfo.Version),
		Protocol: init.ProtocolVersion,
		Tools:    len(tools.Tools),
	}, nil
}

func send(w io.Writer, msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return errors.New("shim stopped: " + err.Error())
	}
	return nil
}
