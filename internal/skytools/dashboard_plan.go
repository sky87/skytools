package skytools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sky87/skytools/internal/termui"
)

func contains(names []string, name string) bool { return slices.Contains(names, name) }

// planItem is one line of Review. A package you selected gets the action its
// state calls for. A dependency you did not select is left alone if installed.
type planItem struct {
	pkg      PackageStatus
	action   stepAction
	chosen   bool
	neededBy []string
	from, to string
}

func (d *dashboardModel) buildPlan(names []string) []planItem {
	catalog := make([]tool, len(d.packages))
	byName := make(map[string]PackageStatus, len(d.packages))
	for i, p := range d.packages {
		catalog[i], byName[p.Name] = tool{name: p.Name}, p
	}
	resolved, err := selectTools(catalog, strings.Join(names, ","), true)
	if err != nil {
		resolved = nil
		for _, t := range catalog {
			if contains(names, t.name) {
				resolved = append(resolved, t)
			}
		}
	}
	plan := make([]planItem, 0, len(resolved))
	for _, t := range resolved {
		p := byName[t.name]
		item := planItem{pkg: p, chosen: contains(names, t.name), from: d.versions[t.name].installed, to: d.versions[t.name].latest}
		switch {
		case !p.Installed && p.Partial:
			item.action = actionRepair
		case !p.Installed:
			item.action = actionInstall
		case item.chosen:
			item.action = actionUpdate
		default:
			item.action = actionKeep
		}
		for _, other := range resolved {
			if contains(packageRequirements[other.name].Tools, t.name) {
				item.neededBy = append(item.neededBy, other.name)
			}
		}
		plan = append(plan, item)
	}
	return plan
}

func (item planItem) command() string {
	if item.action == actionUpdate {
		return "update"
	}
	return "ensure"
}

func planSteps(tools []tool, plan []planItem) ([]planStep, error) {
	byName := make(map[string]tool, len(tools))
	for _, t := range tools {
		byName[t.name] = t
	}
	steps := make([]planStep, 0, len(plan))
	for _, item := range plan {
		t, ok := byName[item.pkg.Name]
		if !ok {
			return nil, fmt.Errorf("required tool %q is absent from the catalog", item.pkg.Name)
		}
		steps = append(steps, planStep{t, item.command()})
	}
	return steps, nil
}

// blockedPlanTools names the planned tools that a missing system package stops.
func (d *dashboardModel) blockedPlanTools() []string {
	var blocked []string
	for _, item := range d.plan {
		for _, dep := range d.check.report.Missing {
			if contains(dep.RequiredBy, item.pkg.Name) && !contains(blocked, item.pkg.Name) {
				blocked = append(blocked, item.pkg.Name)
			}
		}
	}
	return blocked
}

// namesWithoutBlocked drops a selected package when it, or anything it depends
// on, is blocked.
func (d *dashboardModel) namesWithoutBlocked() []string {
	blocked := d.blockedPlanTools()
	var kept []string
	for _, name := range d.planNames {
		clean := true
		for _, item := range d.buildPlan([]string{name}) {
			clean = clean && !contains(blocked, item.pkg.Name)
		}
		if clean {
			kept = append(kept, name)
		}
	}
	return kept
}

type runPhase int

const (
	phaseQueued runPhase = iota
	phaseRunning
	phaseDone
	phaseFailed
	phaseCancelled
	phaseNotStarted
)

type runStep struct {
	name, label string
	action      stepAction
	from, to    string
	phase       runPhase
	started     time.Time
	elapsed     time.Duration
	logFrom     int
}

type dashboardRun struct {
	self                    bool
	steps                   []*runStep
	log                     []string
	partial                 string
	offset                  int
	follow                  bool
	confirmQuit, cancelling bool
	finishedAt              time.Time
	started                 time.Time
	failure                 string
	cancelled               bool
}

func (r *dashboardRun) isFinished() bool { return !r.finishedAt.IsZero() }

func (r *dashboardRun) elapsed() time.Duration {
	if r.isFinished() {
		return r.finishedAt.Sub(r.started)
	}
	return time.Since(r.started)
}

func (r *dashboardRun) step(name string) *runStep {
	for _, step := range r.steps {
		if step.name == name {
			return step
		}
	}
	return nil
}

// beginPlanRun lists what will change. Packages the plan leaves alone still pass
// through performPlan, which verifies them, but they are not shown as work.
func (d *dashboardModel) beginPlanRun() {
	run := &dashboardRun{started: time.Now(), follow: true}
	for _, item := range d.plan {
		if item.action != actionKeep {
			run.steps = append(run.steps, &runStep{name: item.pkg.Name, label: item.pkg.Label, action: item.action, from: item.from, to: item.to})
		}
	}
	d.run, d.screen = run, screenRun
}

func (d *dashboardModel) beginSelfUpdateRun(path string) {
	d.self.executable = path
	now := time.Now()
	d.run = &dashboardRun{self: true, started: now, follow: true, steps: []*runStep{{
		name: "skytools", label: "skytools", action: actionUpdate, from: d.version, to: d.self.latest, phase: phaseRunning, started: now,
	}}}
	d.screen = screenRun
}

func (d *dashboardModel) syncRun(job *dashboardJob) {
	run := d.run
	lines, partial, events := job.log.drain(len(run.log))
	run.log, run.partial = append(run.log, lines...), partial
	for _, event := range events {
		step := run.step(event.Name)
		if step == nil {
			continue
		}
		switch event.Phase {
		case stepStarted:
			step.phase, step.started, step.logFrom, step.action = phaseRunning, time.Now(), event.line, event.Action
		case stepDone:
			step.phase, step.elapsed = phaseDone, event.Elapsed
		case stepFailed:
			step.phase, step.elapsed = phaseFailed, event.Elapsed
		case stepCancelled:
			step.phase, step.elapsed = phaseCancelled, event.Elapsed
		}
	}
}

// finishedStep is what the receipt reports after the terminal is restored.
type finishedStep struct {
	action        stepAction
	name, version string
}

// completeRun rescans real files and changes only the rows that the run touched.
// Upstream's latest versions are still valid, so they are not fetched again.
func (d *dashboardModel) completeRun(job *dashboardJob, err error, packages []PackageStatus) {
	d.syncRun(job)
	run := d.run
	run.finishedAt = time.Now()
	run.confirmQuit, run.cancelling = false, false
	run.cancelled = errors.Is(err, context.Canceled)
	if err != nil && !run.cancelled {
		run.failure = termui.Sanitize(err.Error())
	}
	if run.partial != "" {
		run.log = append(run.log, run.partial)
		run.partial = ""
	}
	if run.self && err == nil {
		run.steps[0].phase = phaseDone
	}
	for _, step := range run.steps {
		switch step.phase {
		case phaseQueued:
			step.phase = phaseNotStarted
		case phaseRunning:
			step.phase = phaseFailed
			if run.cancelled {
				step.phase = phaseCancelled
			}
			step.elapsed = time.Since(step.started)
		case phaseFailed:
			if run.cancelled {
				step.phase = phaseCancelled
			}
		}
	}
	d.packages = packages
	byName := make(map[string]PackageStatus, len(packages))
	for _, p := range packages {
		byName[p.Name] = p
	}
	for _, step := range run.steps {
		p, known := byName[step.name]
		if run.self {
			if step.phase == phaseDone && d.selfUpdateAvailable() {
				d.self.updated = d.self.latest
			}
			continue
		}
		if step.phase == phaseDone {
			delete(d.selected, step.name)
		}
		if known && step.phase != phaseNotStarted {
			d.resetInstalledVersion(p)
		}
		if known && step.phase == phaseDone {
			version := p.Version
			if version == "" {
				version = step.to
			}
			d.finished = append(d.finished, finishedStep{step.action, step.name, version})
		}
	}
	d.screen = screenResult
	d.resultOffset = 0
}

// unfinishedNames is what R retries: every selected step that did not complete.
func (d *dashboardModel) unfinishedNames() []string {
	if d.run == nil {
		return nil
	}
	var names []string
	for _, step := range d.run.steps {
		if step.phase != phaseDone {
			names = append(names, step.name)
		}
	}
	return names
}

var pastTense = map[stepAction]string{actionUpdate: "updated", actionInstall: "installed", actionRepair: "repaired"}

// receipt survives in scrollback after the alternate screen closes. It stays
// empty when nothing happened, so quitting after browsing prints nothing.
func (d *dashboardModel) receipt() []string {
	var lines []string
	for _, action := range []stepAction{actionUpdate, actionInstall, actionRepair} {
		var names []string
		for _, step := range d.finished {
			if step.action == action {
				names = append(names, strings.TrimSpace(step.name+" "+step.version))
			}
		}
		if len(names) > 0 {
			lines = append(lines, "skytools: "+pastTense[action]+" "+strings.Join(names, ", "))
		}
	}
	if d.self.updated != "" {
		lines = append(lines, "skytools: updated skytools to "+d.self.updated+"; the next run uses it")
	}
	if run := d.run; run != nil && run.isFinished() {
		if run.failure != "" {
			lines = append(lines, "skytools: "+run.failure)
		}
		var notStarted []string
		for _, step := range run.steps {
			switch step.phase {
			case phaseCancelled:
				lines = append(lines, "skytools: cancelled during "+step.name)
			case phaseNotStarted:
				notStarted = append(notStarted, step.name)
			}
		}
		if len(notStarted) > 0 {
			lines = append(lines, "skytools: not started: "+strings.Join(notStarted, ", "))
		}
	}
	if d.printMissing {
		lines = append(lines, "skytools: missing system packages: "+strings.Join(d.check.report.packageNames(), ", "))
		lines = append(lines, "  "+d.check.report.InstallCommand)
	}
	if len(d.finished) > 0 {
		lines = append(lines, `skytools: activate in this shell: eval "$(skytools env)"`)
	}
	for i, line := range lines {
		lines[i] = termui.Sanitize(line)
	}
	return lines
}
