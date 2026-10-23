package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"studiolink/internal/hostserver"
)

// Status is what GET /status on the status port returns.
type Status struct {
	Version  string                   `json:"version"`
	PID      int                      `json:"pid"`
	Started  time.Time                `json:"started"`
	Config   string                   `json:"config"`
	Serving  []string                 `json:"serving"`
	Tailnet  string                   `json:"tailnet,omitempty"`
	Relays   []RelayStatus            `json:"relays,omitempty"`
	AuditLog string                   `json:"audit_log,omitempty"`
	Sessions []hostserver.SessionInfo `json:"sessions"`
}

// RelayStatus is one relay tunnel's state.
type RelayStatus struct {
	URL       string    `json:"url"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since"`
	LastError string    `json:"last_error,omitempty"`
}

func (b *bridge) serveStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(b.Status())
}

// runFile records a running bridge's status port next to its config, so
// `studiolink-host status` can find it and a second copy can refuse to start.
type runFile struct {
	path string
	info runInfo
}

type runInfo struct {
	PID       int    `json:"pid"`
	StatusURL string `json:"status_url"`
}

// runFilePath turns ...\studiolink.json into ...\studiolink.run.json.
func runFilePath(configPath string) string {
	return strings.TrimSuffix(configPath, filepath.Ext(configPath)) + ".run.json"
}

var errNotRunning = errors.New("studiolink-host is not running with this config")

func readRunFile(path string) (runInfo, error) {
	var info runInfo
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return info, errNotRunning
	}
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(data, &info); err != nil || info.StatusURL == "" {
		return info, errNotRunning
	}
	return info, nil
}

// alive reports whether a bridge answers on the status port.
func (info runInfo) alive() bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(info.StatusURL + "/ping")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func claimRunFile(path, statusURL string) (*runFile, error) {
	if info, err := readRunFile(path); err == nil && info.alive() {
		return nil, fmt.Errorf("studiolink-host is already running with this config (pid %d); stop it first, or see it with studiolink-host status", info.PID)
	}
	rf := &runFile{path: path, info: runInfo{PID: os.Getpid(), StatusURL: statusURL}}
	data, _ := json.Marshal(rf.info)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", path, err)
	}
	return rf, nil
}

// release removes the run file if it is still this process's.
func (rf *runFile) release() {
	if info, err := readRunFile(rf.path); err == nil && info.PID == rf.info.PID {
		_ = os.Remove(rf.path)
	}
}

// fetchStatus asks the bridge running with the config at cfgPath for its
// status.
func fetchStatus(ctx context.Context, cfgPath, token string) (*Status, error) {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, err
	}
	info, err := readRunFile(runFilePath(abs))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", info.StatusURL+"/status", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, errNotRunning // a stale run file from a crash
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	return &st, nil
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("status", stderr)
	asJSON := fset.Bool("json", false, "print the status as JSON")
	if fset.Parse(args) != nil {
		return 2
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	st, err := fetchStatus(context.Background(), *path, cfg.Token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
		return 0
	}
	printStatus(stdout, st, time.Now())
	return 0
}

func printStatus(w io.Writer, st *Status, now time.Time) {
	fmt.Fprintf(w, "studiolink-host %s, pid %d, up %s\n", st.Version, st.PID, ago(now, st.Started))
	fmt.Fprintf(w, "config:    %s\n", st.Config)
	serving := st.Serving
	if st.Tailnet != "" {
		serving = append(serving, st.Tailnet)
	}
	fmt.Fprintf(w, "serving:   %s\n", strings.Join(serving, ", "))
	for _, r := range st.Relays {
		if r.Connected {
			fmt.Fprintf(w, "relay:     %s connected for %s\n", r.URL, ago(now, r.Since))
		} else {
			fmt.Fprintf(w, "relay:     %s DOWN: %s\n", r.URL, r.LastError)
		}
	}
	if st.AuditLog != "" {
		fmt.Fprintf(w, "audit log: %s\n", st.AuditLog)
	}
	if len(st.Sessions) == 0 {
		fmt.Fprintln(w, "\nNo sessions.")
		return
	}
	fmt.Fprintf(w, "\n%d session(s):\n", len(st.Sessions))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  SESSION\tCLIENT\tFROM\tOPENED\tIDLE\tTOOL CALLS")
	for _, s := range st.Sessions {
		from := s.Remote
		if s.Peer != "" {
			from = s.Peer
		}
		idle := ago(now, s.LastUsed)
		if s.InFlight > 0 {
			idle = fmt.Sprintf("busy (%d)", s.InFlight)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s ago\t%s\t%d\n", s.ID, s.Client, from, ago(now, s.Opened), idle, s.ToolCalls)
	}
	_ = tw.Flush()
}

// ago formats how long ago t was, to the second.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return now.Sub(t).Round(time.Second).String()
}
