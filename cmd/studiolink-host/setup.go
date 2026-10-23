package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"studiolink/internal/config"
	"studiolink/internal/netif"
)

// prompter asks yes/no questions on a console. With yes set, or once input
// runs out, it takes each question's default.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	yes bool
}

func (p *prompter) ask(question string, def bool) bool {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	if p.yes {
		fmt.Fprintf(p.out, "%s %s %s\n", question, hint, answer(def))
		return def
	}
	for {
		fmt.Fprintf(p.out, "%s %s ", question, hint)
		line, err := p.in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		case "":
			if err != nil {
				fmt.Fprintln(p.out, answer(def))
			}
			return def
		}
		if err != nil {
			fmt.Fprintln(p.out)
			return def
		}
	}
}

func answer(b bool) string {
	if b {
		return "y"
	}
	return "n"
}

// addToPath returns the PATH list with dir appended, and whether it was
// missing. Entries compare case-insensitively, ignoring a trailing slash.
func addToPath(list, dir string) (string, bool) {
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), `\/`)) }
	for entry := range strings.SplitSeq(list, ";") {
		if norm(entry) == norm(dir) {
			return list, false
		}
	}
	if list == "" || strings.HasSuffix(list, ";") {
		return list + dir, true
	}
	return list + ";" + dir, true
}

// copyFile copies src to dst through a temporary file, so a failed copy
// never leaves a half-written binary behind.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// installFiles copies exe, and the clients folder next to it if there is
// one, into dir. It returns the installed exe's path.
func installFiles(exe, dir string) (string, error) {
	if err := os.MkdirAll(filepath.Join(dir, "clients"), 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(exe))
	if err := copyFile(exe, dst); err != nil {
		return "", err
	}
	src := filepath.Join(filepath.Dir(exe), "clients")
	entries, err := os.ReadDir(src)
	if errors.Is(err, os.ErrNotExist) {
		return dst, nil
	}
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), "studiolink-client-") {
			if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dir, "clients", e.Name())); err != nil {
				return "", err
			}
		}
	}
	// This PC's own client, on PATH next to the host, for agents here that
	// share a stdio config with containers.
	own := filepath.Join(src, clientFile("windows-"+runtime.GOARCH))
	if _, err := os.Stat(own); err == nil {
		if err := copyFile(own, filepath.Join(dir, "studiolink-client.exe")); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// sameFile reports whether a and b name the same path.
func sameFile(a, b string) bool {
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ia, ib)
}

// wslAdapter returns the WSL2 adapter the config doesn't serve yet, if any.
func wslAdapter(cfg *config.Config, adapters []netif.Adapter) (netif.Adapter, bool) {
	for _, li := range cfg.ListenInterfaces {
		if strings.Contains(strings.ToLower(li.Name), "(wsl") {
			return netif.Adapter{}, false
		}
	}
	for _, a := range adapters {
		if isWSL(a) && a.Up && len(a.IPv4) > 0 {
			return a, true
		}
	}
	return netif.Adapter{}, false
}
