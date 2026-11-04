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

	"github.com/sky87/skytools/internal/termui"
)

func TestDashboardSelectionEligibilityAndDependencyPreview(t *testing.T) {
	d := newDashboardModel([]PackageStatus{
		{Name: "uv"},
		{Name: "node", Installed: true},
		{Name: "pi", Partial: true},
	})
	if len(d.names()) != 0 {
		t.Fatal("dashboard preselected packages")
	}
	d.selectAll()
	if got := strings.Join(d.names(), ","); got != "uv,pi" {
		t.Fatalf("install selection = %s", got)
	}
	if !strings.HasPrefix(d.preview(), "Also included: node") {
		t.Fatalf("dependency absent from preview: %s", d.preview())
	}
	d.setAction(dashboardUpdate)
	if got := strings.Join(d.names(), ","); got != "pi" {
		t.Fatalf("update retained ineligible package: %s", got)
	}
	d.selectAll()
	if got := strings.Join(d.names(), ","); got != "node,pi" {
		t.Fatalf("update selection = %s", got)
	}
	d.selectAll()
	if len(d.names()) != 0 {
		t.Fatal("select all did not clear complete selection")
	}
}

func TestDashboardDependencyVisibleBeforeClippedMetadata(t *testing.T) {
	d := newDashboardModel([]PackageStatus{
		{Name: "node", Installed: true, Label: "Node.js", Channel: "LTS"},
		{Name: "pi", Label: "pi coding agent", Channel: "stable", Partial: true, Path: "/home/developer/" + strings.Repeat("long-path/", 20)},
	})
	d.selected["pi"] = true
	var ui termui.Context
	d.frame(&ui, 80, 24, termui.Event{}, "test")
	text := ui.EndFrame().Text()
	if !strings.Contains(text, "pi") || !strings.Contains(text, "Also included: node") {
		t.Fatalf("automatic dependency was clipped behind metadata: %s", text)
	}
}

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

func TestDashboardJobSuccessFailureCancellationAndRefresh(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		job := startDashboardJob(context.Background(), func(ctx context.Context, output io.Writer, started func(string)) error {
			started("uv")
			fmt.Fprintln(output, "download complete")
			return nil
		})
		err := dashboardWait(t, job)
		current, logs := job.log.snapshot()
		if err != nil || current != "uv" || len(logs) != 1 || logs[0] != "download complete" {
			t.Fatalf("job snapshot: %s, %v, %v", current, logs, err)
		}
		d := newDashboardModel([]PackageStatus{{Name: "uv"}})
		d.selected["uv"], d.busy = true, true
		d.complete(err, []PackageStatus{{Name: "uv", Installed: true}})
		if d.busy || d.failed || d.selected["uv"] || !strings.Contains(d.message, "completed") {
			t.Fatalf("success did not refresh selection: %+v", d)
		}
	})
	t.Run("failure", func(t *testing.T) {
		failure := errors.New("download failed")
		job := startDashboardJob(context.Background(), func(context.Context, io.Writer, func(string)) error { return failure })
		d := newDashboardModel(nil)
		d.complete(dashboardWait(t, job), nil)
		if !d.failed || !strings.Contains(d.message, failure.Error()) {
			t.Fatal("operation failure not shown")
		}
		d.refresh([]PackageStatus{{Name: "uv", Installed: true}})
		if d.failed || len(d.packages) != 1 || !d.packages[0].Installed {
			t.Fatal("refresh did not replace inventory")
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		job := startDashboardJob(context.Background(), func(ctx context.Context, _ io.Writer, _ func(string)) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		<-started
		job.cancel()
		err := dashboardWait(t, job)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
		d := newDashboardModel(nil)
		d.complete(err, nil)
		if d.failed || !strings.Contains(d.message, "cancelled") {
			t.Fatal("cancellation not reported")
		}
	})
}

func TestDashboardLogBoundsAndSanitization(t *testing.T) {
	log := &dashboardLog{}
	for i := 0; i < dashboardLogLines+20; i++ {
		fmt.Fprintf(log, "line %d\n", i)
	}
	log.Write([]byte("\x1b]52;c;clipboard"))
	log.Write([]byte("\a\x1b[31mred\x1b[0m\n"))
	log.Write([]byte(strings.Repeat("x", dashboardLogLineBytes*3)))
	_, lines := log.snapshot()
	if len(log.lines) != dashboardLogLines || len(log.partial) > dashboardLogLineBytes || len(lines) != dashboardLogLines+1 {
		t.Fatalf("unbounded logs: %d lines, %d bytes partial", len(log.lines), len(log.partial))
	}
	if lines[len(lines)-2] != "red" {
		t.Fatalf("split escape sequences leaked: %q", lines[len(lines)-2])
	}
	for _, line := range lines {
		if strings.ContainsAny(line, "\x1b\r\a") {
			t.Fatalf("unsafe log line: %q", line)
		}
	}
	lines[0] = "changed"
	_, again := log.snapshot()
	if again[0] == "changed" {
		t.Fatal("log snapshot shares mutable backing array")
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
	job := startDashboardJob(m.ctx, m.dashboardRunner(dashboardInstall, []string{"uv"}))
	if err := dashboardWait(t, job); err != nil {
		t.Fatal(err)
	}
	if m.ctx != originalContext || m.out != originalOut {
		t.Fatal("worker mutated UI manager")
	}
	if _, lines := job.log.snapshot(); len(lines) == 0 {
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

func TestDashboardFrameActionsScrollAndSmallTerminal(t *testing.T) {
	var packages []PackageStatus
	for i := 0; i < 15; i++ {
		packages = append(packages, PackageStatus{Name: fmt.Sprintf("tool%d", i), Label: "A package", Channel: "stable"})
	}
	packages[0].Installed, packages[0].Version = true, "1.2.3"
	d := newDashboardModel(packages)
	var ui termui.Context
	d.frame(&ui, 80, 24, termui.Event{}, "test")
	initial := ui.EndFrame().Text()
	if !strings.Contains(initial, "Installed") || !strings.Contains(initial, "1.2.3") || !strings.Contains(initial, "No packages selected") {
		t.Fatalf("initial inventory omitted local version/state: %s", initial)
	}
	ui.Focus("package-tool0")
	d.frame(&ui, 80, 24, termui.Event{Key: termui.KeySpace}, "test")
	ui.EndFrame()
	if d.selected["tool0"] {
		t.Fatal("ineligible installed package was selected")
	}
	ui.Focus("package-tool14")
	d.frame(&ui, 80, 24, termui.Event{Key: termui.KeySpace}, "test")
	ui.EndFrame()
	if !d.selected["tool14"] || d.scroll == 0 {
		t.Fatal("keyboard selection did not follow offscreen focused package")
	}
	ui.Focus("run")
	run, refresh := d.frame(&ui, 80, 24, termui.Event{Key: termui.KeyEnter}, "test")
	ui.EndFrame()
	if !run || refresh {
		t.Fatal("Run selected did not execute selected install action")
	}
	ui.Focus("action-refresh")
	run, refresh = d.frame(&ui, 80, 24, termui.Event{Key: termui.KeyEnter}, "test")
	ui.EndFrame()
	if run || !refresh {
		t.Fatal("Refresh did not immediately request a read-only rescan")
	}
	d.busy = true
	d.current = "uv"
	ui.Focus("run")
	run, refresh = d.frame(&ui, 80, 24, termui.Event{Key: termui.KeyEnter}, "test")
	if text := ui.EndFrame().Text(); !strings.Contains(text, "Running Install missing: uv") || !strings.Contains(text, "Ctrl-C cancel") {
		t.Fatalf("operation progress/cancel guidance not rendered: %s", text)
	}
	if run || refresh {
		t.Fatal("busy dashboard started another operation")
	}
	run, refresh = d.frame(&ui, 12, 4, termui.Event{Key: termui.KeyEnter}, "test")
	frame := ui.EndFrame()
	if run || refresh || frame.Width != 12 || frame.Height != 4 {
		t.Fatal("small terminal activated invisible controls or changed frame bounds")
	}
}
