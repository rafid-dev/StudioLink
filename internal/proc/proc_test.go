package proc

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as the child process: TestMain runs a helper
// mode instead of the tests when STUDIOLINK_PROC_HELPER is set.
const helperEnv = "STUDIOLINK_PROC_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
		os.Exit(m.Run())
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "spawn":
		// Start a plain grandchild (no job of its own) that sleeps forever
		// and report its PID.
		os.Setenv(helperEnv, "sleep")
		child := exec.Command(os.Args[0], "-test.run=^$")
		if err := child.Start(); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		fmt.Println(child.Process.Pid)
		time.Sleep(time.Hour)
	case "sleep":
		time.Sleep(time.Hour)
	}
	os.Exit(2)
}

func helperSpec(mode string) Spec {
	os.Setenv(helperEnv, mode)
	return Spec{Path: os.Args[0], Args: []string{"-test.run=^$"}}
}

func TestEcho(t *testing.T) {
	p, err := Start(helperSpec("echo"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	if _, err := io.WriteString(p.Stdin(), "hello\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(p.Stdout()).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "hello\n" {
		t.Errorf("got %q, want %q", line, "hello\n")
	}
	p.Stdin().Close()
	waitDone(t, p)
}

func TestKillEndsGrandchildren(t *testing.T) {
	p, err := Start(helperSpec("spawn"))
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(p.Stdout()).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("helper did not report a PID: %q", line)
	}
	if !alive(grandchild) {
		t.Fatal("grandchild not running before Kill")
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p)
	deadline := time.Now().Add(5 * time.Second)
	for alive(grandchild) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d still running after Kill", grandchild)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := p.Kill(); err != nil {
		t.Errorf("second Kill: %v", err)
	}
}

func waitDone(t *testing.T, p *Process) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
}
