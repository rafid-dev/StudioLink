package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

// stopBridge asks the bridge running with the config at cfgPath to shut
// down and waits until it has. It returns errNotRunning if none is.
func stopBridge(ctx context.Context, cfgPath, token string) error {
	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	info, err := readRunFile(runFilePath(abs))
	if err != nil {
		return err
	}
	if !info.alive() {
		return errNotRunning
	}
	req, err := http.NewRequestWithContext(ctx, "POST", info.StatusURL+"/stop", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("stop: %s", resp.Status)
	}
	// Sessions get a few seconds to end before the bridge exits.
	deadline := time.Now().Add(15 * time.Second)
	for info.alive() {
		if time.Now().After(deadline) {
			return fmt.Errorf("pid %d did not stop within 15 s", info.PID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

func runStop(args []string, stdout, stderr io.Writer) int {
	fset, path := newFlags("stop", stderr)
	if fset.Parse(args) != nil {
		return 2
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch err := stopBridge(context.Background(), *path, cfg.Token); {
	case errors.Is(err, errNotRunning):
		fmt.Fprintln(stdout, "studiolink-host was not running")
	case err != nil:
		fmt.Fprintln(stderr, err)
		return 1
	default:
		fmt.Fprintln(stdout, "studiolink-host stopped")
	}
	return 0
}
