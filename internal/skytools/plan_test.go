package skytools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func planTestManager(t *testing.T) (*manager, *bytes.Buffer) {
	t.Helper()
	m := testManager(t)
	m.env = setEnv(m.env, "PATH", t.TempDir())
	m.systemProvider = packageProviderFunc(func(context.Context, []SystemDependency) (SystemDependencyReport, error) {
		return SystemDependencyReport{Platform: "fixture"}, nil
	})
	output := new(bytes.Buffer)
	m.out, m.errOut = output, output
	return m, output
}

// A tool whose managed status is real, so inventory checks after each step pass.
func planTestTool(m *manager, name string, calls *[]string) tool {
	binary := m.at(".local/bin/" + name)
	install := func(what string) func() error {
		return func() error {
			*calls = append(*calls, what+" "+name)
			fmt.Fprintf(m.out, "%s output\nsecond line", what)
			return atomicWrite(binary, []byte("#!/bin/sh\n"), 0755)
		}
	}
	return tool{name: name, label: name + " label", ensure: install("ensure"), update: install("update")}
}

func TestPerformPlanMixesCommandsAndReportsSteps(t *testing.T) {
	m, output := planTestManager(t)
	var calls []string
	uv, gh, btop := planTestTool(m, "uv", &calls), planTestTool(m, "gh", &calls), planTestTool(m, "btop", &calls)
	if err := atomicWrite(m.at(".local/bin/gh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(m.at(".local/bin/btop"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	var events []string
	err := m.performPlan([]planStep{{uv, "ensure"}, {gh, "update"}, {btop, "ensure"}}, func(e stepEvent) {
		events = append(events, fmt.Sprintf("%d %s %s", e.Phase, e.Name, e.Action))
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "ensure uv,ensure gh,update gh" {
		t.Fatalf("calls = %s", got)
	}
	want := []string{"0 uv install", "1 uv install", "0 gh update", "1 gh update", "0 btop keep", "1 btop keep"}
	if strings.Join(events, ";") != strings.Join(want, ";") {
		t.Fatalf("events = %v", events)
	}
	text := output.String()
	for _, line := range []string{
		"skytools: prerequisites ok (fixture)\n",
		"[1/3] uv    install\n      ensure output\n      second line\n[1/3] uv    done in",
		"[2/3] gh    update\n",
		"[3/3] btop  already installed\n",
		"skytools: 1 updated, 1 installed, 1 already installed\n",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("missing %q in:\n%s", line, text)
		}
	}
}

func TestPerformPlanStopsAtFailureAndNamesWhatDidNotStart(t *testing.T) {
	m, output := planTestManager(t)
	var calls []string
	failure := errors.New("checksum mismatch")
	uv, gh, btop := planTestTool(m, "uv", &calls), planTestTool(m, "gh", &calls), planTestTool(m, "btop", &calls)
	gh.ensure = func() error { return failure }
	var failed stepEvent
	err := m.performPlan([]planStep{{uv, "ensure"}, {gh, "ensure"}, {btop, "ensure"}}, func(e stepEvent) {
		if e.Phase == stepFailed {
			failed = e
		}
	})
	if !errors.Is(err, failure) || failed.Name != "gh" || !errors.Is(failed.Err, failure) {
		t.Fatalf("err=%v failed=%+v", err, failed)
	}
	if strings.Contains(strings.Join(calls, ","), "btop") {
		t.Fatal("a step ran after the failure")
	}
	if text := output.String(); !strings.Contains(text, "[2/3] gh    failed\n") || !strings.Contains(text, "skytools: 1 installed, 1 failed, 1 not started (btop)\n") {
		t.Fatalf("output:\n%s", text)
	}
	if _, statErr := os.Stat(m.at(".profile")); !os.IsNotExist(statErr) {
		t.Fatal("failed plan configured the shell")
	}
}

func TestPerformPlanCancellationIsNotAFailure(t *testing.T) {
	m, output := planTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.ctx = ctx
	var calls []string
	uv, gh := planTestTool(m, "uv", &calls), planTestTool(m, "gh", &calls)
	uv.ensure = func() error {
		cancel()
		return errors.New("signal: killed")
	}
	var phases []stepPhase
	if err := m.performPlan([]planStep{{uv, "ensure"}, {gh, "ensure"}}, func(e stepEvent) { phases = append(phases, e.Phase) }); err == nil {
		t.Fatal("cancelled plan reported success")
	}
	if fmt.Sprint(phases) != fmt.Sprint([]stepPhase{stepStarted, stepCancelled}) {
		t.Fatalf("phases = %v", phases)
	}
	if text := output.String(); !strings.Contains(text, "[1/2] uv  cancelled\n") || !strings.Contains(text, "2 not started (uv, gh)") || strings.Contains(text, "1 failed") {
		t.Fatalf("output:\n%s", text)
	}
}

func TestIndentWriterHandlesSplitLinesAndSeparateStreams(t *testing.T) {
	var out bytes.Buffer
	w := &indentWriter{out: &out}
	for _, part := range []string{"one", " two\nthree\n", "four"} {
		if n, err := w.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatalf("Write(%q) = %d, %v", part, n, err)
		}
	}
	w.finishLine()
	if got, want := out.String(), "      one two\n      three\n      four\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !sameWriter(&out, &out) || sameWriter(&out, &bytes.Buffer{}) {
		t.Fatal("writer identity")
	}
	if sameWriter(writerFunc(func(p []byte) (int, error) { return len(p), nil }), writerFunc(func(p []byte) (int, error) { return len(p), nil })) {
		t.Fatal("uncomparable writers treated as the same")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestDependenciesForPlanUsesEachStepsCommand(t *testing.T) {
	steps := []planStep{{tool{name: "uv"}, "ensure"}, {tool{name: "gh"}, "update"}, {tool{name: "btop"}, "ensure"}}
	states := []PackageStatus{{Installed: true}, {Installed: true}, {}}
	byCapability := map[string]string{}
	for _, dep := range dependenciesForPlan(steps, states) {
		byCapability[dep.Capability] = strings.Join(dep.RequiredBy, ",")
	}
	if byCapability["certificates"] != "gh,btop" {
		t.Fatalf("certificates required by %q", byCapability["certificates"])
	}
	if _, ok := byCapability["http-client"]; ok {
		t.Fatal("an installed tool under ensure still required its install dependencies")
	}
}
