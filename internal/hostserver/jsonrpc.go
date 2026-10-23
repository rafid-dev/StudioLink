package hostserver

import (
	"bytes"
	"encoding/json"
)

// envelope holds the JSON-RPC fields needed to route a message. Everything
// else passes through untouched.
type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func parseEnvelope(msg []byte) (envelope, error) {
	var e envelope
	err := json.Unmarshal(msg, &e)
	return e, err
}

func (e envelope) hasID() bool {
	return len(e.ID) > 0 && !bytes.Equal(e.ID, []byte("null"))
}

func (e envelope) isRequest() bool  { return e.Method != "" && e.hasID() }
func (e envelope) isResponse() bool { return e.Method == "" && e.hasID() }

// toolName returns params.name for tools/call requests, for logging.
func (e envelope) toolName() string {
	if e.Method != "tools/call" {
		return ""
	}
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(e.Params, &p)
	return p.Name
}

// JSON-RPC error codes used by the bridge.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeInternalError  = -32603
)

// errorResponse builds a JSON-RPC error response. id may be nil.
func errorResponse(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Error: struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{code, message},
	})
	return out
}
