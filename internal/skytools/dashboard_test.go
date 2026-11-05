package skytools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDashboardNonTerminalAndFastStatusAreReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	marker := filepath.Join(home, "executed")
	binary := filepath.Join(home, ".local/bin/uv")
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"ui"}, {"status", "--only", "uv"}} {
		var output, errOutput bytes.Buffer
		if code := Run(context.Background(), args, &output, &errOutput, "test"); code != 0 {
			t.Fatalf("Run(%v) = %d: %s", args, code, errOutput.String())
		}
		text := output.String()
		if !strings.Contains(text, "local package inventory (read-only)") || !strings.Contains(text, "uv") || !strings.Contains(text, "installed") {
			t.Fatalf("missing fast inventory: %s", text)
		}
		if strings.Contains(text, "\x1b") {
			t.Fatal("nonterminal output contains terminal commands")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("read-only inventory executed a binary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".bashrc")); !os.IsNotExist(err) {
		t.Fatalf("read-only inventory configured shell: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local/state/skytools")); !os.IsNotExist(err) {
		t.Fatalf("read-only inventory acquired installation lock: %v", err)
	}
}

func TestDashboardInventorySanitizesMetadata(t *testing.T) {
	var output bytes.Buffer
	printInventory(&output, []PackageStatus{{Name: "uv\x1b[2J", Channel: "stable", Detail: "bad\x1b]52;c;clipboard\a\rmetadata"}})
	if strings.ContainsAny(output.String(), "\x1b\a\r") || strings.Contains(output.String(), "clipboard") {
		t.Fatalf("unsafe inventory output: %q", output.String())
	}
}

func dashboardWait(t *testing.T, job *dashboardJob) error {
	t.Helper()
	select {
	case err := <-job.done:
		return err
	case <-time.After(2 * time.Second):
		job.cancel()
		t.Fatal("dashboard worker did not stop")
		return nil
	}
}

func TestDashboardJobReportsOutputAndErrors(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		job := startDashboardJob(context.Background(), func(ctx context.Context, output io.Writer, report func(stepEvent)) error {
			report(stepEvent{Phase: stepStarted, Name: "uv"})
			fmt.Fprintln(output, "download complete")
			return nil
		})
		if err := dashboardWait(t, job); err != nil {
			t.Fatal(err)
		}
		lines, partial, events := job.log.drain(0)
		if len(lines) != 1 || lines[0] != "download complete" || partial != "" || len(events) != 1 || events[0].Name != "uv" {
			t.Fatalf("job log: %v %q %+v", lines, partial, events)
		}
	})
	t.Run("failure", func(t *testing.T) {
		failure := errors.New("download failed")
		job := startDashboardJob(context.Background(), func(context.Context, io.Writer, func(stepEvent)) error { return failure })
		if err := dashboardWait(t, job); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		job := startDashboardJob(context.Background(), func(ctx context.Context, _ io.Writer, _ func(stepEvent)) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		<-started
		job.cancel()
		if err := dashboardWait(t, job); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	})
}

func TestDashboardLogKeepsEveryLineAndSanitizes(t *testing.T) {
	log := &dashboardLog{}
	const count = 5000
	for i := 0; i < count; i++ {
		fmt.Fprintf(log, "line %d\n", i)
	}
	log.Write([]byte("\x1b]52;c;clipboard"))
	log.Write([]byte("\a\x1b[31mred\x1b[0m\n"))
	log.Write([]byte(strings.Repeat("x", dashboardLogLineBytes*3)))
	lines, partial, _ := log.drain(0)
	if len(lines) != count+1 || lines[0] != "line 0" {
		t.Fatalf("kept %d lines, first %q", len(lines), lines[0])
	}
	if lines[len(lines)-1] != "red" {
		t.Fatalf("split escape sequences leaked: %q", lines[len(lines)-1])
	}
	if len(partial) != dashboardLogLineBytes {
		t.Fatalf("one line can grow without limit: %d bytes", len(partial))
	}
	for _, line := range lines {
		if strings.ContainsAny(line, "\x1b\r\a") {
			t.Fatalf("unsafe log line: %q", line)
		}
	}
	if more, _, _ := log.drain(len(lines)); len(more) != 0 {
		t.Fatalf("drain repeated old lines: %v", more)
	}
}

func TestDashboardSharedOperationValidatesInstallAndKeepsWorkerStateLocal(t *testing.T) {
	m := testManager(t)
	m.env = setEnv(m.env, "PATH", t.TempDir())
	var output bytes.Buffer
	m.out, m.errOut = &output, &output
	installed := m.at(".local/bin/uv")
	create := func() error { return os.WriteFile(installed, []byte("unused"), 0755) }
	ensureCalls, updateCalls := 0, 0
	selected := []tool{{name: "uv", label: "uv", ensure: func() error { ensureCalls++; return create() }, update: func() error { updateCalls++; return nil }}}
	if err := m.perform("ensure", selected, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.perform("ensure", selected, nil); err != nil {
		t.Fatal(err)
	}
	if ensureCalls != 1 || updateCalls != 0 {
		t.Fatalf("ensure called installers %d times, updates %d", ensureCalls, updateCalls)
	}
	if err := m.perform("update", selected, nil); err != nil || updateCalls != 1 {
		t.Fatalf("update did not use shared operation: %v, updates %d", err, updateCalls)
	}
	originalContext, originalOut := m.ctx, m.out
	job := startDashboardJob(m.ctx, m.dashboardRunner([]planItem{{pkg: PackageStatus{Name: "uv"}, action: actionKeep}}))
	if err := dashboardWait(t, job); err != nil {
		t.Fatal(err)
	}
	if m.ctx != originalContext || m.out != originalOut {
		t.Fatal("worker mutated UI manager")
	}
	if lines, _, _ := job.log.drain(0); len(lines) == 0 {
		t.Fatal("worker output was not redirected")
	}
	if err := os.Remove(installed); err != nil {
		t.Fatal(err)
	}
	selected[0].ensure = func() error { return nil }
	if err := m.perform("ensure", selected, nil); err == nil || !strings.Contains(err.Error(), "still missing or incomplete") {
		t.Fatalf("successful installer exit bypassed inventory check: %v", err)
	}
	failure := errors.New("installer failed")
	selected[0].ensure = func() error { return failure }
	if err := m.perform("ensure", selected, nil); !errors.Is(err, failure) {
		t.Fatalf("installer error was lost: %v", err)
	}
}

func TestDashboardEnsureSkipsSystemToolsButUpdateRequiresHomeCopy(t *testing.T) {
	m := testManager(t)
	systemBin := t.TempDir()
	m.env = setEnv(m.env, "PATH", systemBin)
	if err := os.WriteFile(filepath.Join(systemBin, "uv"), []byte("unused"), 0755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	selected := []tool{{name: "uv", label: "uv", ensure: func() error { calls++; return nil }, update: func() error { calls++; return nil }}}
	if err := m.perform("ensure", selected, nil); err != nil || calls != 0 {
		t.Fatalf("ensure changed an installed system package: %v, %d calls", err, calls)
	}
	if err := m.perform("update", selected, nil); err == nil || calls != 2 {
		t.Fatalf("update accepted unchanged system installation: %v, %d calls", err, calls)
	}
}
