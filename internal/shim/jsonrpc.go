package shim

import "encoding/json"

const (
	codeParseError    = -32700
	codeInternalError = -32603
)

// envelope holds the fields the shim routes on.
type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

// isRequest reports whether the message expects a response.
func (e envelope) isRequest() bool {
	return len(e.ID) > 0 && string(e.ID) != "null" && e.Method != ""
}

func errorResponse(id json.RawMessage, code int, msg string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
	return b
}
