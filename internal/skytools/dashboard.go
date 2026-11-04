package skytools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sky87/skytools/internal/termui"
)

type dashboardAction int

const (
	dashboardInstall dashboardAction = iota
	dashboardUpdate
)

func (a dashboardAction) label() string {
	switch a {
	case dashboardUpdate:
		return "Update"
	default:
		return "Install missing"
	}
}

func (a dashboardAction) command() string {
	if a == dashboardUpdate {
		return "update"
	}
	return "ensure"
}

type dashboardModel struct {
	packages          []PackageStatus
	selected          map[string]bool
	action            dashboardAction
	scroll            int
	message           string
	failed            bool
	busy              bool
	current           string
	logs              []string
	versions          map[string]dashboardPackageVersions
	versionGeneration uint64
}

func newDashboardModel(packages []PackageStatus) *dashboardModel {
	d := &dashboardModel{packages: packages, selected: make(map[string]bool), message: "Choose packages and an action, then Run selected."}
	d.resetVersions()
	return d
}

func (d *dashboardModel) eligible(p PackageStatus) bool {
	if d.action == dashboardUpdate {
		return p.Installed || p.Partial
	}
	return !p.Installed
}

func (d *dashboardModel) reconcile() {
	for _, p := range d.packages {
		if !d.eligible(p) {
			delete(d.selected, p.Name)
		}
	}
}

func (d *dashboardModel) setAction(action dashboardAction) {
	d.action = action
	d.reconcile()
}

func (d *dashboardModel) selectAll() {
	all := true
	for _, p := range d.packages {
		if d.eligible(p) && !d.selected[p.Name] {
			all = false
		}
	}
	for _, p := range d.packages {
		if d.eligible(p) {
			d.selected[p.Name] = !all
		}
	}
}

func (d *dashboardModel) names() []string {
	var names []string
	for _, p := range d.packages {
		if d.selected[p.Name] && d.eligible(p) {
			names = append(names, p.Name)
		}
	}
	return names
}

func (d *dashboardModel) preview() string {
	names := d.names()
	if len(names) == 0 {
		return "No packages selected."
	}
	text := "Selected: " + strings.Join(names, ", ")
	var catalog []tool
	for _, p := range d.packages {
		catalog = append(catalog, tool{name: p.Name})
	}
	resolved, err := selectTools(catalog, strings.Join(names, ","), true)
	if err == nil {
		var extra []string
		for _, t := range resolved {
			if !d.selected[t.name] {
				extra = append(extra, t.name)
			}
		}
		if len(extra) > 0 {
			text = "Also included: " + strings.Join(extra, ", ") + " | " + text
		}
	}
	return text
}

func packageState(p PackageStatus) string {
	if p.Partial {
		return "Incomplete"
	}
	if p.Installed {
		return "Installed"
	}
	return "Missing"
}

func packageDetail(p PackageStatus) string {
	var parts []string
	if p.Version != "" {
		parts = append(parts, p.Version)
	}
	if p.Path != "" {
		parts = append(parts, p.Path)
	}
	if p.Detail != "" {
		parts = append(parts, p.Detail)
	}
	return strings.Join(parts, " | ")
}

func printInventory(out io.Writer, packages []PackageStatus) {
	fmt.Fprintln(out, "skytools local package inventory (read-only)")
	for _, p := range packages {
		state := strings.ToLower(packageState(p))
		if !p.Installed && !p.Partial {
			state = "not installed / unavailable"
		}
		detail := packageDetail(p)
		if p.Origin == "system" {
			detail = "system PATH | " + detail
		}
		fmt.Fprintf(out, "%-12s %-27s %-7s %s\n", termui.Sanitize(p.Name), state, termui.Sanitize(p.Channel), termui.Sanitize(detail))
	}
	fmt.Fprintln(out, "Use skytools in an interactive terminal to select packages.")
	fmt.Fprintln(out, "Or use: skytools ensure --only <names> | skytools update --only <names>")
	fmt.Fprintln(out, "Use skytools status --versions for explicit version commands.")
}

// frame declares every control anew, including clipped rows. Only the framework
// retains focus; package selection, scroll position, and operation state live here.
func (d *dashboardModel) frame(ui *termui.Context, width, height int, event termui.Event, version string) (run, refresh bool) {
	ui.BeginFrame(width, height, event)
	ui.Label(1, 0, "skytools "+version+"  |  developer packages", termui.StyleTitle)
	installed, incomplete := 0, 0
	for _, p := range d.packages {
		if p.Installed {
			installed++
		} else if p.Partial {
			incomplete++
		}
	}
	updates, checking := 0, 0
	for _, p := range d.packages {
		_, _, _, status, _ := d.versionCells(p)
		if status == "Update available" {
			updates++
		}
		if d.versions[p.Name].latestChecking {
			checking++
		}
	}
	ui.Label(1, 1, fmt.Sprintf("%d installed  %d incomplete  %d missing  |  %d updates  %d checking", installed, incomplete, len(d.packages)-installed-incomplete, updates, checking), termui.StyleMuted)
	if height < 14 || width < 44 {
		ui.Label(1, 3, "Enlarge terminal to at least 44 x 14.", termui.StyleError)
		ui.Label(1, 5, "q quit | Ctrl-C cancels an operation", termui.StyleMuted)
		return false, false
	}
	if ui.Button("action-install", 1, 3, "Install missing", !d.busy) {
		d.setAction(dashboardInstall)
	}
	if ui.Button("action-update", 21, 3, "Update", !d.busy) {
		d.setAction(dashboardUpdate)
	}
	if ui.Button("action-refresh", 33, 3, "Refresh", !d.busy) {
		refresh = true
	}
	ui.Label(1, 4, "Action: "+d.action.label()+" | Space toggles a focused package", termui.StyleAccent)
	if width >= 80 {
		ui.Label(5, 5, fmt.Sprintf("%-14s %-10s %-11s %-11s %-6s %s", "Package", "State", "Installed", "Latest", "Target", "Update"), termui.StyleMuted)
	} else {
		ui.Label(5, 5, fmt.Sprintf("%-10s %-11s %-11s %s", "Package", "Installed", "Latest", ""), termui.StyleMuted)
	}
	listTop, listHeight := 6, height-14
	if listHeight > len(d.packages) {
		listHeight = len(d.packages)
	}
	if listHeight < 1 {
		listHeight = 1
	}
	for i, p := range d.packages {
		if ui.Focused() == "package-"+p.Name {
			if i < d.scroll {
				d.scroll = i
			} else if i >= d.scroll+listHeight {
				d.scroll = i - listHeight + 1
			}
		}
	}
	if d.scroll > len(d.packages)-listHeight {
		d.scroll = len(d.packages) - listHeight
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
	for i, p := range d.packages {
		value := d.selected[p.Name]
		name := p.Name
		current, latest, channel, status, style := d.versionCells(p)
		label := fmt.Sprintf("%-14s %-10s %-11s %-11s %-6s %s", versionColumn(name, 14), packageState(p), versionColumn(current, 11), versionColumn(latest, 11), channel, status)
		if width < 80 {
			marker := ""
			if status == "Update available" {
				marker = "UPD"
			}
			label = fmt.Sprintf("%-10s %-11s %-11s %s", versionColumn(name, 10), versionColumn(current, 11), versionColumn(latest, 11), marker)
		}
		row := listTop + i - d.scroll
		if i < d.scroll || i >= d.scroll+listHeight {
			row = -1
		}
		if !d.eligible(p) && !d.busy {
			ui.FocusableLabel("package-"+p.Name, 1, row, "[-] "+label, termui.StyleMuted)
		} else if ui.Checkbox("package-"+p.Name, 1, row, label, &value, !d.busy && d.eligible(p)) {
			d.selected[p.Name] = value
		}
		if width >= 80 {
			ui.Label(43, row, fmt.Sprintf("%-11s", versionColumn(latest, 11)), style)
			ui.Label(62, row, status, style)
		} else if status == "Update available" {
			ui.Label(40, row, "UPD", style)
		}
	}
	bottom := listTop + listHeight
	ui.Label(1, bottom, fmt.Sprintf("Packages %d-%d of %d  |  %s", d.scroll+1, d.scroll+listHeight, len(d.packages), d.preview()), termui.StyleAccent)
	detail := "Focus a package to see its local version and path."
	for _, p := range d.packages {
		if ui.Focused() == "package-"+p.Name {
			detail = d.versionDetail(p) + " | " + packageDetail(p)
			if dependencies := packageRequirements[p.Name].Tools; len(dependencies) > 0 {
				detail = "Requires: " + strings.Join(dependencies, ", ") + " | " + detail
			}
			if detail == "" {
				detail = p.Label + ": no local version or path available."
			}
		}
	}
	ui.Label(1, bottom+1, detail, termui.StyleMuted)
	systemUpdate := false
	if d.action == dashboardUpdate {
		for _, p := range d.packages {
			if d.selected[p.Name] && p.Origin == "system" {
				systemUpdate = true
			}
		}
	}
	if systemUpdate {
		ui.Label(1, bottom+2, "Updates install a newer copy in your home.", termui.StyleAccent)
	} else {
		ui.Label(1, bottom+2, "Install keeps existing versions. Updates use LTS/stable channels.", termui.StyleMuted)
	}
	if ui.Button("run", 1, bottom+3, "Run selected", !d.busy && len(d.names()) > 0) {
		run = true
	}
	status, style := d.message, termui.StyleSuccess
	if d.failed {
		style = termui.StyleError
	}
	if d.busy {
		status, style = "Running "+d.action.label()+": "+d.current+" | Ctrl-C cancel; q cancel and quit", termui.StyleAccent
	}
	ui.Label(1, bottom+4, status, style)
	logRows := height - (bottom + 6)
	if logRows > 3 {
		logRows = 3
	}
	start := len(d.logs) - logRows
	if start < 0 {
		start = 0
	}
	for i := start; i < len(d.logs); i++ {
		ui.Label(1, bottom+5+i-start, d.logs[i], termui.StyleMuted)
	}
	ui.Label(1, height-1, "Tab/arrows move | Space select | i/u action | a all | r scan | q quit", termui.StyleMuted)
	return run, refresh
}

const dashboardLogLines = 120
const dashboardLogLineBytes = 4096

// dashboardLog is a bounded, concurrent writer shared by stdout and stderr.
// Escape sequences are removed only after complete chunks have been joined.
type dashboardLog struct {
	mu      sync.Mutex
	lines   []string
	partial string
	current string
}

func (l *dashboardLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, part := range strings.SplitAfter(string(data), "\n") {
		complete := strings.HasSuffix(part, "\n")
		part = strings.TrimSuffix(part, "\n")
		l.partial += part
		if len(l.partial) > dashboardLogLineBytes {
			l.partial = l.partial[len(l.partial)-dashboardLogLineBytes:]
		}
		if complete {
			l.lines = append(l.lines, termui.Sanitize(l.partial))
			if len(l.lines) > dashboardLogLines {
				copy(l.lines, l.lines[len(l.lines)-dashboardLogLines:])
				l.lines = l.lines[:dashboardLogLines]
			}
			l.partial = ""
		}
	}
	// A bounded substring can still retain the whole Write buffer. Detach the
	// remaining fragment so a long final line cannot keep that allocation alive.
	l.partial = strings.Clone(l.partial)
	return len(data), nil
}

func (l *dashboardLog) setCurrent(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.current = name
}

func (l *dashboardLog) snapshot() (string, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := append([]string{}, l.lines...)
	if l.partial != "" {
		lines = append(lines, termui.Sanitize(l.partial))
	}
	return termui.Sanitize(l.current), lines
}

type dashboardRunner func(context.Context, io.Writer, func(string)) error

type dashboardJob struct {
	cancel context.CancelFunc
	done   chan error
	log    *dashboardLog
}

func startDashboardJob(ctx context.Context, runner dashboardRunner) *dashboardJob {
	jobCtx, cancel := context.WithCancel(ctx)
	job := &dashboardJob{cancel: cancel, done: make(chan error, 1), log: &dashboardLog{current: "waiting for installation lock"}}
	go func() {
		defer cancel()
		err := runner(jobCtx, job.log, job.log.setCurrent)
		if jobCtx.Err() != nil {
			err = jobCtx.Err()
		}
		job.done <- err
	}()
	return job
}

func (m *manager) dashboardRunner(action dashboardAction, names []string) dashboardRunner {
	// Copy manager fields, then construct closures bound to that copy. Installers
	// must never borrow the UI manager's context or output writers.
	return func(ctx context.Context, output io.Writer, started func(string)) error {
		worker := *m
		worker.ctx, worker.out, worker.errOut = ctx, output, output
		worker.env = append([]string{}, m.env...)
		selected, err := selectTools(worker.tools(), strings.Join(names, ","), true)
		if err != nil {
			return err
		}
		return worker.perform(action.command(), selected, started)
	}
}

func (d *dashboardModel) complete(err error, packages []PackageStatus) {
	d.busy = false
	d.packages = packages
	d.reconcile()
	d.failed = err != nil && !errors.Is(err, context.Canceled)
	switch {
	case errors.Is(err, context.Canceled):
		d.message = "Operation cancelled. Local package inventory refreshed."
	case err != nil:
		d.message = "Failed: " + termui.Sanitize(err.Error())
	default:
		d.message = "Operation completed. Local package inventory refreshed."
	}
}

func (d *dashboardModel) refresh(packages []PackageStatus) {
	d.packages = packages
	d.reconcile()
	d.message, d.failed = "Local package inventory refreshed.", false
}

func (m *manager) dashboard(version string, input *os.File, output io.Writer) (result error) {
	return m.dashboardWithVersions(version, input, output, m.versionProbes())
}

func (m *manager) dashboardWithVersions(version string, input *os.File, output io.Writer, probes dashboardVersionProbes) (result error) {
	packages := m.scan(m.tools())
	file, ok := output.(*os.File)
	if !ok || !termui.IsTerminal(input) || !termui.IsTerminal(file) {
		printInventory(output, packages)
		return nil
	}
	terminal, err := termui.Open(input, file)
	if err != nil {
		return err
	}
	defer func() {
		if err := terminal.Close(); result == nil {
			result = err
		}
	}()
	d := newDashboardModel(packages)
	var ui termui.Context
	ui.Focus("action-install")
	var job *dashboardJob
	var checker *dashboardVersionCheck
	var retired []*dashboardVersionCheck
	restartCheck := true
	stopCheck := func() {
		if checker != nil {
			checker.cancel()
			retired = append(retired, checker)
			checker = nil
		}
	}
	defer func() {
		stopCheck()
		for _, check := range retired {
			check.cancel()
			<-check.done
		}
		if job != nil {
			job.cancel()
			<-job.done
		}
	}()
	refresh := func() {
		stopCheck()
		d.refresh(m.scan(m.tools()))
		d.resetVersions()
		restartCheck = true
	}
	event := termui.Event{}
	quitting := false
	for {
		alive := retired[:0]
		for _, check := range retired {
			select {
			case <-check.done:
			default:
				alive = append(alive, check)
			}
		}
		retired = alive
		for checker != nil {
			select {
			case update, ok := <-checker.results:
				if !ok {
					retired = append(retired, checker)
					checker = nil
				} else {
					d.applyVersion(update)
				}
			default:
				goto versionsDrained
			}
		}
	versionsDrained:
		if job != nil {
			d.current, d.logs = job.log.snapshot()
			select {
			case err := <-job.done:
				d.current, d.logs = job.log.snapshot()
				job = nil
				d.complete(err, m.scan(m.tools()))
				d.resetVersions()
				restartCheck = true
				if quitting {
					return nil
				}
			default:
			}
		}
		if event.Key == termui.KeyCtrlC {
			if job != nil {
				job.cancel()
				d.current = "cancelling..."
			} else {
				return nil
			}
		}
		if event.Key == termui.KeyRune {
			switch event.Rune {
			case 'q', 'Q':
				if job == nil {
					return nil
				}
				job.cancel()
				quitting = true
			case 'a', 'A':
				if job == nil {
					d.selectAll()
				}
			case 'i', 'I':
				if job == nil {
					d.setAction(dashboardInstall)
				}
			case 'u', 'U':
				if job == nil {
					d.setAction(dashboardUpdate)
				}
			case 'r', 'R':
				if job == nil {
					refresh()
				}
			}
		}
		width, height := terminal.Size()
		run, rescan := d.frame(&ui, width, height, event, version)
		if rescan {
			refresh()
		}
		if run && job == nil {
			stopCheck()
			restartCheck = false
			d.busy, d.failed, d.logs = true, false, nil
			job = startDashboardJob(m.ctx, m.dashboardRunner(d.action, d.names()))
		}
		if err := terminal.Present(ui.EndFrame()); err != nil {
			return err
		}
		// The first local screen is presented before any upstream request or
		// executable version query is started.
		if restartCheck && job == nil {
			checker = startDashboardVersions(m.ctx, d.versionGeneration, append([]PackageStatus{}, d.packages...), probes)
			restartCheck = false
		}
		event, err = terminal.Poll(m.ctx, 75*time.Millisecond)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
	}
}
