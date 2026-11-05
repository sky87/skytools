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
	"unicode"

	"github.com/sky87/skytools/internal/termui"
)

type dashboardScreen int

const (
	screenPackages dashboardScreen = iota
	screenDetails
	screenReview
	screenRun
	screenResult
	screenLog
	screenHelp
	screenPrerequisites
	screenSelfUpdate
)

// dashboardCommand is what a key asks the event loop to do beyond changing the
// model: everything that starts or stops background work.
type dashboardCommand int

const (
	commandNone dashboardCommand = iota
	commandQuit
	commandRefresh
	commandCheckPlan
	commandCheckAll
	commandRun
	commandSelfUpdate
	commandCancel
	commandCancelAndQuit
)

const (
	filterAll = iota
	filterUpdates
	filterMissing
	filterIssues
	filterCount
)

// Below this size the dashboard asks for a larger terminal.
const (
	dashboardMinWidth  = 36
	dashboardMinHeight = 10
)

type prerequisiteState struct {
	checking      bool
	generation    uint64
	report        SystemDependencyReport
	err           error
	offset        int
	unhookedShell []string
}

func (s prerequisiteState) blocked() bool {
	return !s.checking && s.err == nil && len(s.report.Missing) > 0
}

type selfUpdateState struct {
	probe, checking bool
	latest          string
	err             error
	executable      string
	updated         string
}

// dashboardModel holds all application state. termui retains only keyboard focus.
type dashboardModel struct {
	packages          []PackageStatus
	selected          map[string]bool
	versions          map[string]dashboardPackageVersions
	versionGeneration uint64
	version, home     string
	width, height     int

	screen     dashboardScreen
	filter     int
	cursor     int
	cursorName string
	scroll     int
	find       string
	finding    bool
	toast      string
	readOnly   string

	detailOffset, helpOffset, selfOffset int
	logOffset                            int
	logReturn                            dashboardScreen
	resultOffset                         int

	planNames []string
	plan      []planItem
	check     prerequisiteState

	run          *dashboardRun
	finished     []finishedStep
	printMissing bool

	self selfUpdateState
}

func newDashboardModel(packages []PackageStatus) *dashboardModel {
	d := &dashboardModel{packages: packages, selected: make(map[string]bool), width: 80, height: 24}
	d.resetVersions()
	return d
}

func (d *dashboardModel) tooSmall() bool {
	return d.width < dashboardMinWidth || d.height < dashboardMinHeight
}

func (d *dashboardModel) inFilter(p PackageStatus, filter int) bool {
	state := d.row(p).state
	switch filter {
	case filterUpdates:
		return state == stateUpdate
	case filterMissing:
		return state == stateMissing
	case filterIssues:
		return state == stateIncomplete || state == stateCheckFailed || state == stateUnknown
	}
	return true
}

func (d *dashboardModel) visible() []PackageStatus {
	var rows []PackageStatus
	find := strings.ToLower(d.find)
	for _, p := range d.packages {
		if d.inFilter(p, d.filter) && (find == "" || strings.Contains(strings.ToLower(p.Name+" "+p.Label), find)) {
			rows = append(rows, p)
		}
	}
	return rows
}

func (d *dashboardModel) focusedPackage() (PackageStatus, bool) {
	rows := d.visible()
	if d.cursor < 0 || d.cursor >= len(rows) {
		return PackageStatus{}, false
	}
	return rows[d.cursor], true
}

// names returns the selection in catalog order.
func (d *dashboardModel) names() []string {
	var names []string
	for _, p := range d.packages {
		if d.selected[p.Name] {
			names = append(names, p.Name)
		}
	}
	return names
}

func (d *dashboardModel) toggleAll(rows []PackageStatus) {
	all := len(rows) > 0
	for _, p := range rows {
		all = all && d.selected[p.Name]
	}
	for _, p := range rows {
		if all {
			delete(d.selected, p.Name)
		} else {
			d.selected[p.Name] = true
		}
	}
}

func (d *dashboardModel) inTab(filter int) []PackageStatus {
	var rows []PackageStatus
	for _, p := range d.packages {
		if d.inFilter(p, filter) {
			rows = append(rows, p)
		}
	}
	return rows
}

func (d *dashboardModel) setFilter(filter int) {
	d.filter = (filter + filterCount) % filterCount
	d.cursor, d.cursorName, d.scroll = 0, "", 0
}

func (d *dashboardModel) moveFocus(step int) {
	if rows := d.visible(); len(rows) > 0 {
		d.cursor = (d.cursor + step + len(rows)) % len(rows)
		d.cursorName = rows[d.cursor].Name
		d.detailOffset = 0
	}
}

func (d *dashboardModel) openReview(names []string) dashboardCommand {
	d.planNames = names
	d.plan = d.buildPlan(names)
	d.check = prerequisiteState{checking: true, generation: d.check.generation + 1}
	d.screen = screenReview
	return commandCheckPlan
}

func (d *dashboardModel) applyPrerequisites(result prerequisiteResult) {
	if result.generation != d.check.generation {
		return
	}
	d.check.checking, d.check.report, d.check.err = false, result.report, result.err
}

// update handles every key except list movement, which belongs to termui's List
// and Scroll controls during the frame.
func (d *dashboardModel) update(event termui.Event) dashboardCommand {
	if event.Key == termui.KeyNone || event.Key == termui.KeyResize {
		return commandNone
	}
	d.toast = ""
	key := keyRune(event)
	if d.tooSmall() {
		switch {
		case d.screen == screenRun:
			if event.Key == termui.KeyCtrlC {
				return commandCancel
			}
		case event.Key == termui.KeyCtrlC || key == 'q':
			return commandQuit
		}
		return commandNone
	}
	switch d.screen {
	case screenPackages:
		return d.updatePackages(event, key)
	case screenDetails:
		return d.updateDetails(event, key)
	case screenReview:
		return d.updateReview(event, key)
	case screenRun:
		return d.updateRun(event, key)
	case screenResult:
		return d.updateResult(event, key)
	case screenLog:
		if event.Key == termui.KeyCtrlC {
			return commandQuit
		}
		if event.Key == termui.KeyEscape || event.Key == termui.KeyLeft || key == 'q' {
			d.screen = d.logReturn
		}
	case screenHelp:
		if event.Key == termui.KeyCtrlC {
			return commandQuit
		}
		if !isScrollKey(event) {
			d.screen = screenPackages
		}
	case screenPrerequisites:
		switch {
		case event.Key == termui.KeyCtrlC:
			return commandQuit
		case event.Key == termui.KeyEscape, event.Key == termui.KeyLeft:
			d.screen = screenPackages
		case key == 'r':
			d.check = prerequisiteState{checking: true, generation: d.check.generation + 1}
			return commandCheckAll
		case key == 'q':
			d.printMissing = d.check.blocked()
			return commandQuit
		}
	case screenSelfUpdate:
		switch {
		case event.Key == termui.KeyCtrlC:
			return commandQuit
		case event.Key == termui.KeyEscape, event.Key == termui.KeyLeft, key == 'q':
			d.screen = screenPackages
		case event.Key == termui.KeyEnter:
			return commandSelfUpdate
		}
	}
	return commandNone
}

func keyRune(event termui.Event) rune {
	if event.Key != termui.KeyRune {
		return 0
	}
	return unicode.ToLower(event.Rune)
}

func isScrollKey(event termui.Event) bool {
	switch event.Key {
	case termui.KeyUp, termui.KeyDown, termui.KeyPageUp, termui.KeyPageDown, termui.KeyHome, termui.KeyEnd:
		return true
	case termui.KeyRune:
		return event.Rune == 'j' || event.Rune == 'k' || event.Rune == 'g' || event.Rune == 'G'
	}
	return false
}

func (d *dashboardModel) updatePackages(event termui.Event, key rune) dashboardCommand {
	if event.Key == termui.KeyCtrlC {
		return commandQuit
	}
	if d.finding {
		switch event.Key {
		case termui.KeyEscape:
			d.finding, d.find = false, ""
		case termui.KeyEnter:
			d.finding = false
		case termui.KeyBackspace:
			if runes := []rune(d.find); len(runes) > 0 {
				d.find = string(runes[:len(runes)-1])
			}
		case termui.KeyRune:
			d.find += string(event.Rune)
		}
		return commandNone
	}
	focused, hasFocus := d.focusedPackage()
	switch event.Key {
	case termui.KeyTab:
		d.setFilter(d.filter + 1)
	case termui.KeyBackTab:
		d.setFilter(d.filter - 1)
	case termui.KeyRight:
		if hasFocus {
			d.screen, d.detailOffset = screenDetails, 0
		}
	case termui.KeyEscape:
		if d.find != "" {
			d.find = ""
		} else {
			clear(d.selected)
		}
	case termui.KeySpace:
		if d.readOnly == "" && hasFocus {
			if d.selected[focused.Name] {
				delete(d.selected, focused.Name)
			} else {
				d.selected[focused.Name] = true
			}
		}
	case termui.KeyEnter:
		if d.readOnly != "" {
			return commandNone
		}
		if names := d.names(); len(names) > 0 {
			return d.openReview(names)
		}
		if !hasFocus {
			return commandNone
		}
		switch d.row(focused).state {
		case stateCurrent:
			d.toast = focused.Name + " is current. Space selects it for a forced update."
		case stateAhead:
			d.toast = focused.Name + " is ahead of its target. Space selects it to install the target."
		default:
			return d.openReview([]string{focused.Name})
		}
	}
	switch key {
	case 'q':
		return commandQuit
	case 'r':
		return commandRefresh
	case '/':
		d.finding = true
	case '?':
		d.screen, d.helpOffset = screenHelp, 0
	case 'p':
		d.screen = screenPrerequisites
		d.check = prerequisiteState{checking: true, generation: d.check.generation + 1}
		return commandCheckAll
	case 's':
		d.screen, d.selfOffset = screenSelfUpdate, 0
	case 'l':
		if d.run != nil && d.run.isFinished() {
			d.openLog(screenPackages)
		} else {
			d.toast = "No run yet, so there is no log."
		}
	case 'u', 'm', 'a':
		if d.readOnly != "" {
			return commandNone
		}
		rows, empty := d.inTab(filterUpdates), "No known updates."
		if key == 'm' {
			rows, empty = d.inTab(filterMissing), "Nothing is missing."
		} else if key == 'a' {
			rows, empty = d.visible(), "Nothing in this view."
		}
		if len(rows) == 0 {
			d.toast = empty
		}
		d.toggleAll(rows)
	}
	return commandNone
}

func (d *dashboardModel) updateDetails(event termui.Event, key rune) dashboardCommand {
	switch {
	case event.Key == termui.KeyCtrlC:
		return commandQuit
	case event.Key == termui.KeyEscape, event.Key == termui.KeyLeft, key == 'q':
		d.screen = screenPackages
	case event.Key == termui.KeyUp, event.Key == termui.KeyRune && event.Rune == 'k':
		d.moveFocus(-1)
	case event.Key == termui.KeyDown, event.Key == termui.KeyRune && event.Rune == 'j':
		d.moveFocus(1)
	case event.Key == termui.KeyEnter:
		if focused, ok := d.focusedPackage(); ok && d.readOnly == "" {
			return d.openReview([]string{focused.Name})
		}
	}
	return commandNone
}

func (d *dashboardModel) updateReview(event termui.Event, key rune) dashboardCommand {
	switch {
	case event.Key == termui.KeyCtrlC:
		return commandQuit
	case event.Key == termui.KeyEscape, event.Key == termui.KeyLeft:
		d.screen = screenPackages
	case key == 'q':
		d.printMissing = d.check.blocked()
		return commandQuit
	case event.Key == termui.KeyEnter:
		if d.check.checking {
			d.toast = "Still checking system prerequisites."
		} else if d.check.err == nil && !d.check.blocked() {
			return commandRun
		}
	case key == 'r':
		if !d.check.checking {
			d.check = prerequisiteState{checking: true, generation: d.check.generation + 1, unhookedShell: d.check.unhookedShell}
			return commandCheckPlan
		}
	case key == 'x':
		if d.check.blocked() {
			names := d.namesWithoutBlocked()
			for _, name := range d.planNames {
				if !contains(names, name) {
					delete(d.selected, name)
				}
			}
			if len(names) == 0 {
				d.screen = screenPackages
				return commandNone
			}
			return d.openReview(names)
		}
	}
	return commandNone
}

func (d *dashboardModel) updateRun(event termui.Event, key rune) dashboardCommand {
	run := d.run
	if run == nil {
		return commandNone
	}
	if event.Key == termui.KeyCtrlC {
		run.confirmQuit, run.cancelling = false, true
		return commandCancel
	}
	if run.confirmQuit {
		switch {
		case key == 'y':
			run.confirmQuit, run.cancelling = false, true
			return commandCancelAndQuit
		case key == 'n', event.Key == termui.KeyEscape:
			run.confirmQuit = false
		}
		return commandNone
	}
	if key == 'q' {
		run.confirmQuit = true
	}
	return commandNone
}

func (d *dashboardModel) updateResult(event termui.Event, key rune) dashboardCommand {
	switch {
	case event.Key == termui.KeyCtrlC, key == 'q':
		return commandQuit
	case event.Key == termui.KeyEnter, event.Key == termui.KeyEscape:
		d.screen = screenPackages
	case key == 'l':
		d.openLog(screenResult)
	case key == 'r':
		if names := d.unfinishedNames(); len(names) > 0 && !d.run.self {
			d.planNames, d.plan = names, d.buildPlan(names)
			return commandRun
		}
	}
	return commandNone
}

func (d *dashboardModel) openLog(back dashboardScreen) {
	d.screen, d.logReturn = screenLog, back
	d.logOffset = len(d.run.log) // Clamped to the last page while drawing.
}

func (d *dashboardModel) refresh(packages []PackageStatus) {
	d.packages = packages
	d.resetVersions()
	d.toast = "Rescanned local files. Checking versions again."
}

// Each line is capped because installers can print progress with no newline. The
// number of lines is not capped, so a cause early in a noisy run stays readable.
const dashboardLogLineBytes = 4096

type loggedStepEvent struct {
	stepEvent
	line int
}

// dashboardLog is a concurrent writer shared by stdout and stderr. Escape
// sequences are removed only after complete chunks have been joined.
type dashboardLog struct {
	mu      sync.Mutex
	lines   []string
	partial string
	events  []loggedStepEvent
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
			l.partial = ""
		}
	}
	// A bounded substring can still retain the whole Write buffer. Detach the
	// remaining fragment so a long final line cannot keep that allocation alive.
	l.partial = strings.Clone(l.partial)
	return len(data), nil
}

func (l *dashboardLog) report(event stepEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, loggedStepEvent{event, len(l.lines)})
}

// drain returns only what is new, so a long log costs nothing per frame.
func (l *dashboardLog) drain(from int) (lines []string, partial string, events []loggedStepEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if from < len(l.lines) {
		lines = append(lines, l.lines[from:]...)
	}
	events, l.events = l.events, nil
	return lines, termui.Sanitize(l.partial), events
}

type dashboardRunner func(context.Context, io.Writer, func(stepEvent)) error

type dashboardJob struct {
	cancel context.CancelFunc
	done   chan error
	log    *dashboardLog
}

func startDashboardJob(ctx context.Context, runner dashboardRunner) *dashboardJob {
	jobCtx, cancel := context.WithCancel(ctx)
	job := &dashboardJob{cancel: cancel, done: make(chan error, 1), log: &dashboardLog{}}
	go func() {
		defer cancel()
		err := runner(jobCtx, job.log, job.log.report)
		if jobCtx.Err() != nil {
			err = jobCtx.Err()
		}
		job.done <- err
	}()
	return job
}

// Copy manager fields, then construct closures bound to that copy. Installers
// must never borrow the UI manager's context or output writers.
func (m *manager) dashboardRunner(plan []planItem) dashboardRunner {
	return func(ctx context.Context, output io.Writer, report func(stepEvent)) error {
		worker := *m
		worker.ctx, worker.out, worker.errOut = ctx, output, output
		worker.env = append([]string{}, m.env...)
		steps, err := planSteps(worker.tools(), plan)
		if err != nil {
			return err
		}
		return worker.performPlan(steps, report)
	}
}

func (m *manager) selfUpdateRunner(current, path string) dashboardRunner {
	return func(ctx context.Context, output io.Writer, _ func(stepEvent)) error {
		worker := *m
		worker.ctx, worker.out, worker.errOut = ctx, output, output
		worker.env = append([]string{}, m.env...)
		return worker.selfUpdate(current, path)
	}
}

// readOnlyReason finds out on open what would otherwise fail only after Run.
func (m *manager) readOnlyReason() string {
	if err := m.platform(); err != nil {
		return "Install and Update are not available on this platform. Browsing works."
	}
	if _, err := m.systemPackageProvider(); err != nil {
		return "Install and Update need Debian-family Linux. Browsing works."
	}
	return ""
}

type prerequisiteResult struct {
	generation uint64
	report     SystemDependencyReport
	err        error
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
	var d *dashboardModel
	// Deferred first so it runs last: the receipt must reach the normal screen,
	// after the alternate screen is gone and the terminal modes are restored.
	defer func() {
		if d != nil {
			for _, line := range d.receipt() {
				fmt.Fprintln(output, line)
			}
		}
	}()
	defer func() {
		if err := terminal.Close(); result == nil {
			result = err
		}
	}()
	d = newDashboardModel(packages)
	d.version, d.home, d.readOnly = version, m.home, m.readOnlyReason()
	d.self.probe = probes.self != nil
	d.self.checking = d.self.probe
	var ui termui.Context
	ui.SetDisplay(termui.DisplayFromEnv(os.Getenv))
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
	prerequisiteCtx, cancelPrerequisites := context.WithCancel(m.ctx)
	prerequisites := make(chan prerequisiteResult, 1)
	var prerequisiteChecks sync.WaitGroup
	checkPrerequisites := func(generation uint64, steps []planStep) {
		prerequisiteChecks.Add(1)
		go func() {
			defer prerequisiteChecks.Done()
			worker := *m
			worker.ctx, worker.out, worker.errOut = prerequisiteCtx, io.Discard, io.Discard
			report, err := worker.checkPlanDependencies(steps)
			select {
			case prerequisites <- prerequisiteResult{generation, report, err}:
			case <-prerequisiteCtx.Done():
			}
		}()
	}
	defer func() {
		cancelPrerequisites()
		prerequisiteChecks.Wait()
		stopCheck()
		for _, check := range retired {
			check.cancel()
			<-check.done
		}
		if job != nil {
			job.cancel()
			jobErr := <-job.done
			d.completeRun(job, jobErr, m.scan(m.tools()))
		}
	}()
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
		select {
		case update := <-prerequisites:
			d.applyPrerequisites(update)
		default:
		}
		if job != nil {
			d.syncRun(job)
			select {
			case jobErr := <-job.done:
				d.completeRun(job, jobErr, m.scan(m.tools()))
				job = nil
				restartCheck = true
				if quitting {
					return nil
				}
			default:
			}
		}
		width, height := terminal.Size()
		d.width, d.height = width, height
		switch d.update(event) {
		case commandQuit:
			return nil
		case commandRefresh:
			stopCheck()
			d.refresh(m.scan(m.tools()))
			restartCheck = true
		case commandCheckPlan:
			d.check.unhookedShell = m.profilesWithoutHook()
			steps, err := planSteps(m.tools(), d.plan)
			if err != nil {
				d.applyPrerequisites(prerequisiteResult{generation: d.check.generation, err: err})
			} else {
				checkPrerequisites(d.check.generation, steps)
			}
		case commandCheckAll:
			var steps []planStep
			for _, t := range m.tools() {
				steps = append(steps, planStep{t, "update"})
			}
			checkPrerequisites(d.check.generation, steps)
		case commandRun:
			stopCheck()
			restartCheck = false
			d.beginPlanRun()
			job = startDashboardJob(m.ctx, m.dashboardRunner(d.plan))
		case commandSelfUpdate:
			path, err := os.Executable()
			if err != nil {
				d.toast = "Cannot find the skytools executable: " + err.Error()
				break
			}
			stopCheck()
			restartCheck = false
			d.beginSelfUpdateRun(path)
			job = startDashboardJob(m.ctx, m.selfUpdateRunner(version, path))
		case commandCancel:
			if job != nil {
				job.cancel()
			}
		case commandCancelAndQuit:
			if job != nil {
				job.cancel()
				quitting = true
			}
		}
		d.frame(&ui, width, height, event)
		if err := terminal.Present(ui.EndFrame()); err != nil {
			return err
		}
		// The first local screen is presented before any upstream request or
		// executable version query is started.
		if restartCheck && job == nil {
			checker = startDashboardVersionTasks(m.ctx, d.versionGeneration, d.pendingVersionTasks(), probes)
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
