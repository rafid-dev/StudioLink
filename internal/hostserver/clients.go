package hostserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"studiolink/internal/audit"
	"studiolink/internal/policy"
)

// OwnerName names the main token's client in logs and the audit log.
const OwnerName = "owner"

// Client is an extra token with its own name and tool policy, for example a
// read-only token for an agent on a shared server.
type Client struct {
	Name  string
	Token string
	Tools *policy.Tools
}

type client struct {
	name  string
	token []byte
	tools *policy.Tools
	owner bool
}

type clientKey struct{}

func clientFrom(ctx context.Context) *client {
	c, _ := ctx.Value(clientKey{}).(*client)
	return c
}

// authenticate returns the client whose token the request carries.
func (s *Server) authenticate(r *http.Request) *client {
	got := r.Header.Get("X-MCP-Auth")
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = bearer
	}
	var found *client
	// Compare against every token so timing does not reveal which matched.
	for _, c := range s.clients {
		if len(c.token) > 0 && subtle.ConstantTimeCompare([]byte(got), c.token) == 1 {
			found = c
		}
	}
	return found
}

// OwnerOnly serves next only to requests carrying the main token, for
// endpoints such as status that show every client's sessions.
func (s *Server) OwnerOnly(next http.Handler) http.Handler {
	return s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := clientFrom(r.Context()); c == nil || !c.owner {
			http.Error(w, "this endpoint needs the main token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// codeInvalidParams is what MCP servers return for an unknown tool.
const codeInvalidParams = -32602

// filterToolList removes tools the policy forbids from a tools/list
// response. Anything it cannot parse passes through unchanged; calls to
// hidden tools are refused anyway.
func filterToolList(msg []byte, tools *policy.Tools) []byte {
	var resp map[string]json.RawMessage
	if json.Unmarshal(msg, &resp) != nil || resp["result"] == nil {
		return msg
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(resp["result"], &result) != nil {
		return msg
	}
	var list []json.RawMessage
	if json.Unmarshal(result["tools"], &list) != nil {
		return msg
	}
	kept := make([]json.RawMessage, 0, len(list))
	for _, t := range list {
		var tool struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(t, &tool) == nil && tools.Allows(tool.Name) {
			kept = append(kept, t)
		}
	}
	var err error
	if result["tools"], err = json.Marshal(kept); err != nil {
		return msg
	}
	if resp["result"], err = json.Marshal(result); err != nil {
		return msg
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return msg
	}
	return out
}

// toolResult classifies a tools/call response for the audit log.
func toolResult(msg []byte) (result, errMsg string) {
	if msg == nil {
		return audit.ResultNoReply, ""
	}
	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal(msg, &resp) != nil {
		return audit.ResultOK, ""
	}
	switch {
	case resp.Error != nil:
		return audit.ResultError, resp.Error.Message
	case resp.Result.IsError:
		return audit.ResultToolError, ""
	}
	return audit.ResultOK, ""
}

// maxAuditArgs caps recorded tool arguments; scripts and images can be big.
const maxAuditArgs = 64 << 10

func toolArguments(params json.RawMessage) json.RawMessage {
	var p struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.Arguments) == 0 {
		return nil
	}
	if len(p.Arguments) > maxAuditArgs {
		note, _ := json.Marshal(fmt.Sprintf("<%d bytes, not recorded>", len(p.Arguments)))
		return note
	}
	return p.Arguments
}

// SessionInfo describes an open session, for status output.
type SessionInfo struct {
	ID        string    `json:"id"` // first 8 characters, as in the logs
	Client    string    `json:"client"`
	Remote    string    `json:"remote"`
	Peer      string    `json:"peer,omitempty"`
	Opened    time.Time `json:"opened"`
	LastUsed  time.Time `json:"last_used"`
	InFlight  int       `json:"in_flight"`
	ToolCalls int64     `json:"tool_calls"`
}
