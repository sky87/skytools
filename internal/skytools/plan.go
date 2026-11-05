package skytools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// planStep is one tool and the command to apply to it. A dashboard plan can mix
// commands: updating rust does not have to touch a dependency it leaves alone.
type planStep struct {
	tool    tool
	command string
}

type stepPhase int

const (
	stepStarted stepPhase = iota
	stepDone
	stepFailed
	stepCancelled
)

// stepAction is what a step really does, which only the current inventory can
// say: "ensure" on an installed tool leaves it alone.
type stepAction string

const (
	actionUpdate  stepAction = "update"
	actionInstall stepAction = "install"
	actionRepair  stepAction = "repair"
	actionKeep    stepAction = "keep"
)

func stepActionFor(command string, before PackageStatus) stepAction {
	switch {
	case command == "update":
		return actionUpdate
	case before.Installed:
		return actionKeep
	case before.Partial:
		return actionRepair
	}
	return actionInstall
}

// stepEvent reports progress to the dashboard. Output goes to manager.out as
// usual, so the command line needs no reporter.
type stepEvent struct {
	Phase       stepPhase
	Name, Label string
	Action      stepAction
	Err         error
	Elapsed     time.Duration
}

// performPlan checks prerequisites for every step before changing anything,
// stops at the first failure, and configures the shell last.
func (m *manager) performPlan(steps []planStep, report func(stepEvent)) error {
	if report == nil {
		report = func(stepEvent) {}
	}
	if err := m.platform(); err != nil {
		return err
	}
	return m.withLock(func() error {
		if err := m.ctx.Err(); err != nil {
			return err
		}
		if err := m.preflight(steps); err != nil {
			return err
		}
		width := 0
		for _, step := range steps {
			width = max(width, len(step.tool.name))
		}
		summary := planSummary{}
		for i, step := range steps {
			position := fmt.Sprintf("[%d/%d] %-*s", i+1, len(steps), width, step.tool.name)
			err := m.ctx.Err()
			if err == nil {
				err = m.performStep(position, step, report, &summary)
			}
			if err != nil {
				for _, rest := range steps[i+1:] {
					summary.notStarted = append(summary.notStarted, rest.tool.name)
				}
				if errors.Is(m.ctx.Err(), context.Canceled) {
					summary.notStarted = append([]string{step.tool.name}, summary.notStarted...)
				} else {
					summary.failed++
				}
				m.log("%s", summary)
				return err
			}
		}
		if err := m.ctx.Err(); err != nil {
			return err
		}
		if err := m.setupShell(); err != nil {
			return err
		}
		m.log("shell configured; open a new shell or run: eval \"$(skytools env)\"")
		if len(steps) > 0 {
			m.log("%s", summary)
		}
		return nil
	})
}

func (m *manager) performStep(position string, step planStep, report func(stepEvent), summary *planSummary) error {
	t, start := step.tool, time.Now()
	before := m.scan([]tool{t})[0]
	action := stepActionFor(step.command, before)
	event := stepEvent{Name: t.name, Label: t.label, Action: action}
	emit := func(phase stepPhase, err error) {
		event.Phase, event.Err, event.Elapsed = phase, err, time.Since(start)
		report(event)
	}
	emit(stepStarted, nil)
	if action == actionKeep {
		fmt.Fprintf(m.out, "%s  already installed\n", position)
	} else {
		fmt.Fprintf(m.out, "%s  %s\n", position, action)
	}
	err := m.runStep(step, action)
	if err == nil {
		after := m.scan([]tool{t})[0]
		if !after.Installed || (action != actionKeep && after.Origin != "managed") {
			err = fmt.Errorf("%s: installation is still missing or incomplete", t.name)
		}
	}
	if err == nil {
		err = m.ctx.Err()
	}
	switch {
	case errors.Is(m.ctx.Err(), context.Canceled):
		fmt.Fprintf(m.out, "%s  cancelled\n", position)
		emit(stepCancelled, err)
	case err != nil:
		fmt.Fprintf(m.out, "%s  failed\n", position)
		emit(stepFailed, err)
	default:
		if action != actionKeep {
			fmt.Fprintf(m.out, "%s  done in %s\n", position, time.Since(start).Round(time.Second))
		}
		summary.add(action)
		emit(stepDone, nil)
	}
	return err
}

// runStep indents installer output under its step. Tool closures hold this
// manager, so its writers are swapped in place for the length of the step.
func (m *manager) runStep(step planStep, action stepAction) error {
	if action == actionKeep {
		return nil
	}
	out, errOut := m.out, m.errOut
	stdout := &indentWriter{out: out}
	stderr := stdout
	if !sameWriter(out, errOut) {
		stderr = &indentWriter{out: errOut}
	}
	m.out, m.errOut = stdout, stderr
	defer func() {
		m.out, m.errOut = out, errOut
		stdout.finishLine()
		stderr.finishLine()
	}()
	t := step.tool
	if err := t.ensure(); err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	if step.command == "update" && t.update != nil {
		if err := t.update(); err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
	}
	return nil
}

// sameWriter tolerates writer types that cannot be compared. Passing one writer
// for both streams keeps os/exec from interleaving partial lines.
func sameWriter(a, b io.Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

const stepOutputIndent = "      "

type indentWriter struct {
	mu      sync.Mutex
	out     io.Writer
	midLine bool
}

func (w *indentWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" {
			continue
		}
		if !w.midLine {
			b.WriteString(stepOutputIndent)
		}
		b.WriteString(line)
		w.midLine = !strings.HasSuffix(line, "\n")
	}
	if _, err := io.WriteString(w.out, b.String()); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (w *indentWriter) finishLine() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.midLine {
		io.WriteString(w.out, "\n")
		w.midLine = false
	}
}

type planSummary struct {
	updated, installed, repaired, kept, failed int
	notStarted                                 []string
}

func (s *planSummary) add(action stepAction) {
	switch action {
	case actionUpdate:
		s.updated++
	case actionInstall:
		s.installed++
	case actionRepair:
		s.repaired++
	case actionKeep:
		s.kept++
	}
}

func (s planSummary) String() string {
	var parts []string
	for _, part := range []struct {
		count int
		word  string
	}{{s.updated, "updated"}, {s.installed, "installed"}, {s.repaired, "repaired"}, {s.kept, "already installed"}, {s.failed, "failed"}} {
		if part.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.count, part.word))
		}
	}
	if len(s.notStarted) > 0 {
		parts = append(parts, fmt.Sprintf("%d not started (%s)", len(s.notStarted), strings.Join(s.notStarted, ", ")))
	}
	if len(parts) == 0 {
		return "nothing to do"
	}
	return strings.Join(parts, ", ")
}
