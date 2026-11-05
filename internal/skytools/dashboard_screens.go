package skytools

import (
	"fmt"
	"strings"
	"time"

	"github.com/sky87/skytools/internal/termui"
)

// painter draws in the usable columns: termui keeps the final column empty.
type painter struct {
	ui   *termui.Context
	w, h int
}

func (p painter) put(x, y int, text string, style termui.Style) int {
	p.ui.Label(x, y, text, style)
	return x + termui.Width(text)
}

func (p painter) rule(y int) { p.ui.Label(0, y, strings.Repeat("─", max(0, p.w)), termui.StyleMuted) }

func (p painter) right(y int, text string, style termui.Style) {
	p.put(p.w-termui.Width(text)-1, y, text, style)
}

// hints shows as many keys as fit, so the most useful ones go first.
func (p painter) hints(hints ...[2]string) {
	x, y := 1, p.h-1
	for _, hint := range hints {
		if x+termui.Width(hint[0])+1+termui.Width(hint[1]) > p.w {
			break
		}
		x = p.put(x, y, hint[0], termui.StyleTitle)
		x = p.put(x+1, y, hint[1], termui.StyleMuted) + 2
	}
}

func (p painter) header(title, right string) {
	p.put(1, 0, title, termui.StyleTitle)
	if p.w >= 60 && right != "" {
		p.right(0, right, termui.StyleMuted)
	}
	p.rule(1)
}

type textRun struct {
	x     int
	text  string
	style termui.Style
}

type textLine []textRun

func line(x int, text string, style termui.Style) textLine {
	return textLine{{x, text, style}}
}

func wrapped(x int, text string, style termui.Style, width int) []textLine {
	var lines []textLine
	for _, part := range termui.Wrap(text, width-x-1) {
		lines = append(lines, line(x, part, style))
	}
	return lines
}

func keyValue(x int, key, value string, style termui.Style, keyWidth, width int) []textLine {
	var lines []textLine
	for i, part := range termui.Wrap(value, width-x-keyWidth-1) {
		row := textLine{{x + keyWidth, part, style}}
		if i == 0 {
			row = append(row, textRun{x, key, termui.StyleMuted})
		}
		lines = append(lines, row)
	}
	return lines
}

// drawLines shows lines between top and bottom as a scrollable control, so long
// content such as a browser package list can always be read in full.
func (p painter) drawLines(id string, lines []textLine, top, bottom int, offset *int) (overflow bool) {
	page := max(1, bottom-top)
	p.ui.Scroll(id, offset, len(lines), page, true)
	for i := 0; i < page && *offset+i < len(lines); i++ {
		for _, run := range lines[*offset+i] {
			p.put(run.x, top+i, run.text, run.style)
		}
	}
	if len(lines) > page {
		p.right(max(0, top-1), fmt.Sprintf(" %d-%d of %d ", *offset+1, min(*offset+page, len(lines)), len(lines)), termui.StyleMuted)
		return true
	}
	return false
}

const spinnerFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

func spinner() string {
	frames := []rune(spinnerFrames)
	return string(frames[int(time.Now().UnixMilli()/100)%len(frames)])
}

func clock(duration time.Duration) string {
	seconds := int(duration.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}

func (d *dashboardModel) shorten(path string) string {
	if d.home != "" && (path == d.home || strings.HasPrefix(path, d.home+"/")) {
		return "~" + strings.TrimPrefix(path, d.home)
	}
	return path
}

// frame declares this screen's controls anew. termui retains only focus.
func (d *dashboardModel) frame(ui *termui.Context, width, height int, event termui.Event) {
	d.width, d.height = width, height
	if d.screen == screenDetails && (event.Key == termui.KeyUp || event.Key == termui.KeyDown || event.Key == termui.KeyRune) {
		event = termui.Event{} // These keys move between packages on this screen.
	}
	ui.BeginFrame(width, height, event)
	p := painter{ui, width - 1, height}
	if d.tooSmall() {
		p.put(1, 0, "Terminal too small", termui.StyleError)
		p.put(1, 1, fmt.Sprintf("Need %d × %d, have %d × %d.", dashboardMinWidth, dashboardMinHeight, width, height), termui.StyleNormal)
		if d.screen == screenRun {
			p.put(1, 2, "ctrl-c cancel", termui.StyleMuted)
		} else {
			p.put(1, 2, "q quit", termui.StyleMuted)
		}
		return
	}
	switch d.screen {
	case screenPackages:
		d.framePackages(p)
	case screenDetails:
		d.frameDetails(p)
	case screenReview:
		d.frameReview(p)
	case screenRun:
		d.frameRun(p)
	case screenResult:
		d.frameResult(p)
	case screenLog:
		d.frameLog(p)
	case screenHelp:
		d.frameHelp(p)
	case screenPrerequisites:
		d.framePrerequisites(p)
	case screenSelfUpdate:
		d.frameSelfUpdate(p)
	}
}

func (d *dashboardModel) stateLabel(p PackageStatus, short bool) (string, termui.Style) {
	text, style := "", termui.StyleMuted
	switch d.row(p).state {
	case stateUpdate:
		text, style = "↑ update", termui.StyleAccentBold
	case stateCurrent:
		text, style = "✓ current", termui.StyleSuccess
	case stateMissing:
		text = "○ missing"
	case stateIncomplete:
		text, style = "! incomplete", termui.StyleWarningBold
	case stateCheckFailed:
		text, style = "? check failed", termui.StyleWarning
	case stateUnknown:
		text, style = "? unknown", termui.StyleWarning
	case stateAhead:
		text = "» ahead"
	default:
		text = spinner() + " checking"
	}
	if p.Origin == "system" && p.Installed {
		if short {
			text += " sys"
		} else {
			text += " · system"
		}
	}
	return text, style
}

// drawPackage draws one row in the columns available, then lets focus replace
// every style at once so the bar has no holes.
func (d *dashboardModel) drawPackage(p painter, y, width int, pkg PackageStatus, focused bool) {
	row := d.row(pkg)
	nameStyle, installedStyle := termui.StyleNormal, termui.StyleNormal
	if row.state == stateMissing {
		nameStyle, installedStyle = termui.StyleMuted, termui.StyleMuted
	}
	installed, latest, latestStyle, arrow := row.installed, row.latest, termui.StyleMuted, ""
	if installed == "" {
		installed = "—"
	}
	switch {
	case d.versions[pkg.Name].latestChecking:
		latest = spinner()
	case row.state == stateUpdate:
		arrow, latestStyle = "→", termui.StyleAccentBold
	case row.state == stateCurrent:
		latest = ""
	case row.state == stateCheckFailed || row.state == stateUnknown:
		latestStyle = termui.StyleWarning
	}
	type cell struct {
		x     int
		text  string
		style termui.Style
	}
	label, labelStyle := d.stateLabel(pkg, width < 72)
	cells := []cell{}
	if focused {
		cells = append(cells, cell{1, "›", termui.StyleTitle})
	}
	if d.selected[pkg.Name] {
		cells = append(cells, cell{3, "●", termui.StyleAccentBold})
	}
	switch {
	case width >= 72:
		cells = append(cells,
			cell{5, termui.Truncate(pkg.Name, 15), nameStyle}, cell{21, termui.Truncate(installed, 11), installedStyle},
			cell{33, arrow, termui.StyleAccent}, cell{35, termui.Truncate(latest, 11), latestStyle},
			cell{47, row.channel, termui.StyleMuted}, cell{54, label, labelStyle})
	case width >= 56:
		cells = append(cells,
			cell{5, termui.Truncate(pkg.Name, 14), nameStyle}, cell{20, termui.Truncate(installed, 10), installedStyle},
			cell{31, arrow, termui.StyleAccent}, cell{33, termui.Truncate(latest, 10), latestStyle}, cell{44, label, labelStyle})
	default:
		version, versionStyle := installed, termui.StyleMuted
		if row.state == stateUpdate {
			version, versionStyle = latest, termui.StyleAccentBold
		} else if row.state == stateMissing || row.state == stateIncomplete {
			version = latest
		}
		glyph, _, _ := strings.Cut(label, " ")
		cells = append(cells,
			cell{5, termui.Truncate(pkg.Name, 14), nameStyle}, cell{20, termui.Truncate(version, width-25), versionStyle},
			cell{width - 2, glyph, labelStyle})
	}
	for _, c := range cells {
		if c.x < width {
			p.put(c.x, y, termui.Truncate(c.text, width-c.x), c.style)
		}
	}
	if focused {
		p.ui.Highlight(y, 0, width, termui.StyleFocused)
	}
}

func (d *dashboardModel) inspector(pkg PackageStatus) (string, string, termui.Style) {
	where := d.shorten(pkg.Path)
	if pkg.Origin == "system" && pkg.Installed {
		where += " (system PATH)"
	}
	first := pkg.Name + ": " + pkg.Label
	if where != "" {
		first += " · " + where
	}
	row, style := d.row(pkg), termui.StyleMuted
	second := ""
	switch row.state {
	case stateUpdate:
		if pkg.Origin == "system" {
			second, style = "Update adds a copy in your home. "+d.shorten(pkg.Path)+" stays.", termui.StyleWarning
		} else {
			second = fmt.Sprintf("Update %s → %s (%s). Enter reviews it.", row.installed, row.latest, row.channel)
		}
	case stateCurrent:
		second = "Up to date with " + row.channel + "."
	case stateMissing:
		second = "Not installed."
		if row.latest != "" && !d.versions[pkg.Name].latestChecking {
			second = fmt.Sprintf("Installs %s (%s).", row.latest, row.channel)
		}
		if tools := packageRequirements[pkg.Name].Tools; len(tools) > 0 {
			second += " Needs " + strings.Join(tools, ", ") + "."
		}
		second += " Enter reviews it."
	case stateIncomplete:
		second = "Incomplete installation; repair required. Enter reviews the repair."
	case stateCheckFailed:
		second, style = "Version check failed; → shows why. r checks again.", termui.StyleWarning
	case stateUnknown:
		second = "Cannot compare these versions; → shows both."
	case stateAhead:
		second = "Newer than the " + row.channel + " target."
	default:
		second = "Checking versions in the background."
	}
	return first, second, style
}

func (d *dashboardModel) checkedSummary() (text string, style termui.Style, busy bool) {
	total, pending, failed := len(d.packages), 0, 0
	for _, p := range d.packages {
		v := d.versions[p.Name]
		if v.latestChecking || v.installedChecking {
			pending++
		} else if v.latestErr != nil || v.installedErr != nil {
			failed++
		}
	}
	switch {
	case pending > 0:
		return fmt.Sprintf("%s checking %d/%d", spinner(), total-pending, total), termui.StyleMuted, true
	case failed > 0:
		return fmt.Sprintf("%d/%d checked · %d failed", total-failed, total, failed), termui.StyleWarning, false
	}
	return fmt.Sprintf("%d/%d checked", total, total), termui.StyleMuted, false
}

func (d *dashboardModel) framePackages(p painter) {
	wide, tall := p.w >= 110, p.h >= 30
	listWidth := p.w
	if wide {
		listWidth = 72
	}
	// Title row.
	x := p.put(1, 0, "skytools", termui.StyleTitle)
	x = p.put(x+1, 0, d.version, termui.StyleMuted)
	if p.w >= 60 && d.selfUpdateAvailable() {
		p.put(x+1, 0, "· "+d.self.latest+" available (S)", termui.StyleAccent)
	}
	summary, summaryStyle, busy := d.checkedSummary()
	if p.w >= 60 {
		p.right(0, summary, summaryStyle)
	} else if busy {
		p.right(0, spinner(), termui.StyleMuted)
	}
	// View tabs.
	x = 0
	for i, tab := range [][2]string{{"All", "All"}, {"Updates", "Upd"}, {"Missing", "Miss"}, {"Issues", "Iss"}} {
		name := tab[0]
		if p.w < 56 {
			name = tab[1]
		}
		count := len(d.inTab(i))
		style := termui.StyleMuted
		switch {
		case d.filter == i:
			style = termui.StyleFocused
		case i == filterUpdates && count > 0:
			style = termui.StyleAccent
		case i == filterIssues && count > 0:
			style = termui.StyleWarning
		}
		x = p.put(x, 1, fmt.Sprintf(" %s %d ", name, count), style)
		if p.w >= 56 {
			x++
		}
	}
	if d.find != "" && !d.finding && p.w-x > 8 {
		p.put(x+1, 1, termui.Truncate("/ "+d.find, p.w-x-2), termui.StyleAccent)
	}
	top := 2
	if tall {
		for _, heading := range []struct {
			x    int
			text string
		}{{5, "PACKAGE"}, {21, "INSTALLED"}, {35, "LATEST"}, {47, "TARGET"}, {54, "STATUS"}} {
			if heading.x < listWidth {
				p.put(heading.x, 2, heading.text, termui.StyleMuted)
			}
		}
		top = 3
	}
	showInspector := !wide && p.h >= 16
	footer := 2
	if showInspector {
		footer = 4
	}
	listHeight := max(1, p.h-top-footer)
	// The cursor follows its package even if background checks change the rows
	// of a filtered view.
	rows := d.visible()
	for i, row := range rows {
		if row.Name == d.cursorName {
			d.cursor = i
		}
	}
	p.ui.List("packages", &d.cursor, len(rows), listHeight, !d.finding)
	if d.cursor < len(rows) {
		d.cursorName = rows[d.cursor].Name
	}
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	} else if d.cursor >= d.scroll+listHeight {
		d.scroll = d.cursor - listHeight + 1
	}
	d.scroll = max(0, min(d.scroll, len(rows)-listHeight))
	for i := 0; i < listHeight && d.scroll+i < len(rows); i++ {
		d.drawPackage(p, top+i, listWidth, rows[d.scroll+i], d.scroll+i == d.cursor)
	}
	if len(rows) == 0 {
		p.put(5, top, "Nothing in this view. Tab changes view.", termui.StyleMuted)
	}
	focused, hasFocus := d.focusedPackage()
	if wide {
		for y := 2; y < p.h-2; y++ {
			p.put(listWidth+1, y, "│", termui.StyleMuted)
		}
		if hasFocus {
			d.drawSidePane(p, listWidth+3, focused)
		}
	}
	ruleY := p.h - footer
	p.rule(ruleY)
	d.drawStatusRule(p, ruleY, listHeight, len(rows))
	if showInspector {
		first, second, style := "", "", termui.StyleMuted
		if hasFocus {
			first, second, style = d.inspector(focused)
		}
		p.put(1, ruleY+1, termui.Truncate(first, p.w-2), termui.StyleNormal)
		p.put(1, ruleY+2, termui.Truncate(second, p.w-2), style)
	}
	switch {
	case d.finding:
		p.hints([2]string{"type", "to filter"}, [2]string{"enter", "keep"}, [2]string{"esc", "clear"})
	case d.readOnly != "":
		p.hints([2]string{"↑↓", "move"}, [2]string{"tab", "view"}, [2]string{"→", "details"}, [2]string{"r", "check"}, [2]string{"?", "help"}, [2]string{"q", "quit"})
	case p.w >= 88:
		p.hints([2]string{"space", "select"}, [2]string{"u", "updates"}, [2]string{"m", "missing"}, [2]string{"a", "all"}, [2]string{"enter", "review"}, [2]string{"→", "details"}, [2]string{"/", "find"}, [2]string{"r", "check"}, [2]string{"?", "help"}, [2]string{"q", "quit"})
	case p.w >= 72:
		p.hints([2]string{"space", "select"}, [2]string{"u", "updates"}, [2]string{"m", "missing"}, [2]string{"enter", "review"}, [2]string{"→", "info"}, [2]string{"?", "help"}, [2]string{"q", "quit"})
	case p.w >= 54:
		p.hints([2]string{"space", "select"}, [2]string{"enter", "review"}, [2]string{"u", "updates"}, [2]string{"?", "help"}, [2]string{"q", "quit"})
	default:
		p.hints([2]string{"?", "help"}, [2]string{"q", "quit"}, [2]string{"space", "pick"}, [2]string{"enter", "go"})
	}
}

// drawStatusRule puts the one thing to notice right now into the separator.
func (d *dashboardModel) drawStatusRule(p painter, y, listHeight, rowCount int) {
	message := func(text string, style termui.Style) {
		p.put(1, y, termui.Truncate(" "+text+" ", p.w-2), style)
	}
	allMissing := len(d.packages) > 0
	for _, pkg := range d.packages {
		allMissing = allMissing && !pkg.Installed && !pkg.Partial
	}
	names := d.names()
	switch {
	case d.finding:
		message("/ "+d.find+"█", termui.StyleTitle)
	case d.toast != "":
		message(d.toast, termui.StyleWarning)
	case d.readOnly != "":
		message(d.readOnly, termui.StyleWarning)
	case len(names) > 0:
		text := fmt.Sprintf("%d selected: %s", len(names), strings.Join(names, ", "))
		var extra []string
		for _, item := range d.buildPlan(names) {
			if !item.chosen {
				extra = append(extra, item.pkg.Name)
			}
		}
		if len(extra) > 0 {
			text += " + " + strings.Join(extra, ", ")
		}
		const hint = " enter review "
		p.put(1, y, termui.Truncate(" "+text+" ", p.w-len(hint)-3), termui.StyleAccentBold)
		p.right(y, hint, termui.StyleTitle)
	case allMissing:
		message("Nothing installed yet. a selects all, enter reviews.", termui.StyleAccent)
	case rowCount > listHeight:
		p.right(y, fmt.Sprintf(" %d-%d of %d ", d.scroll+1, min(d.scroll+listHeight, rowCount), rowCount), termui.StyleMuted)
	}
}

func (d *dashboardModel) drawSidePane(p painter, x int, pkg PackageStatus) {
	width := p.w - x
	y := 2
	p.put(x, y, pkg.Name, termui.StyleTitle)
	p.put(x, y+1, termui.Truncate(pkg.Label, width), termui.StyleMuted)
	label, style := d.stateLabel(pkg, false)
	p.put(x, y+3, label, style)
	y += 5
	for _, row := range d.detailLines(pkg, 0, width) {
		if y >= p.h-2 {
			break
		}
		for _, run := range row {
			p.put(x+run.x, y, run.text, run.style)
		}
		y++
	}
}

func (d *dashboardModel) detailLines(pkg PackageStatus, x, width int) []textLine {
	const keyWidth = 15
	row, versions := d.row(pkg), d.versions[pkg.Name]
	var lines []textLine
	add := func(key, value string, style termui.Style) {
		if value == "" {
			value = "—"
		}
		lines = append(lines, keyValue(x, key, value, style, keyWidth, width+x)...)
	}
	installed := row.installed
	switch {
	case !pkg.Installed && !pkg.Partial:
		installed = "not installed"
	case pkg.Partial:
		installed = "incomplete"
	case pkg.Origin == "system":
		installed += " (system PATH)"
	default:
		installed += " (in your home)"
	}
	add("Installed", installed, termui.StyleNormal)
	add("Latest", row.latest+" ("+row.channel+")", termui.StyleNormal)
	if versions.installedErr != nil {
		add("Installed check", versions.installedErr.Error(), termui.StyleWarning)
	}
	if versions.latestErr != nil {
		add("Latest check", versions.latestErr.Error(), termui.StyleWarning)
	}
	if row.state == stateAhead {
		add("Note", "The installed version is newer than the "+row.channel+" target.", termui.StyleNormal)
	}
	add("Command", d.shorten(pkg.Path), termui.StyleNormal)
	if pkg.Detail != "" {
		add("Detail", pkg.Detail, termui.StyleNormal)
	}
	requirements := packageRequirements[pkg.Name]
	requires := strings.Join(requirements.Tools, ", ")
	if requires == "" {
		requires = "nothing"
	}
	add("Requires", requires, termui.StyleNormal)
	var neededBy []string
	for _, other := range d.packages {
		if contains(packageRequirements[other.Name].Tools, pkg.Name) {
			neededBy = append(neededBy, other.Name)
		}
	}
	if len(neededBy) == 0 {
		neededBy = []string{"nothing"}
	}
	add("Needed by", strings.Join(neededBy, ", "), termui.StyleNormal)
	add("Install needs", strings.Join(requirements.Install, ", "), termui.StyleNormal)
	runtime := strings.Join(requirements.Runtime, ", ")
	if runtime == "" {
		runtime = "nothing"
	}
	add("Runtime needs", runtime, termui.StyleNormal)
	if versions.source != "" {
		add("Version from", versions.source, termui.StyleNormal)
	}
	command := "update"
	if !pkg.Installed {
		command = "ensure"
	}
	add("Same as", "skytools "+command+" --only "+pkg.Name, termui.StyleNormal)
	return lines
}

func (d *dashboardModel) frameDetails(p painter) {
	pkg, ok := d.focusedPackage()
	if !ok {
		d.screen = screenPackages
		d.framePackages(p)
		return
	}
	x := p.put(1, 0, pkg.Name, termui.StyleTitle)
	label, style := d.stateLabel(pkg, false)
	p.put(x+2, 0, termui.Truncate(pkg.Label, p.w-x-termui.Width(label)-5), termui.StyleMuted)
	p.right(0, label, style)
	p.rule(1)
	p.drawLines("details", d.detailLines(pkg, 1, p.w-2), 2, p.h-1, &d.detailOffset)
	p.hints([2]string{"esc", "back"}, [2]string{"↑↓", "prev/next package"}, [2]string{"pgup pgdn", "scroll"}, [2]string{"enter", "review"})
}

func (d *dashboardModel) prerequisiteLines(intro string, width int) []textLine {
	lines := wrapped(1, intro, termui.StyleNormal, width)
	lines = append(lines, nil)
	keyWidth := min(18, width/2)
	for _, dep := range d.check.report.Missing {
		lines = append(lines, keyValue(1, termui.Truncate(dep.Capability, keyWidth-1), strings.Join(dep.Packages, ", "), termui.StyleWarning, keyWidth, width)...)
	}
	lines = append(lines, nil)
	lines = append(lines, wrapped(1, "Run this in another shell, then press r:", termui.StyleNormal, width)...)
	lines = append(lines, wrapped(3, d.check.report.InstallCommand, termui.StyleTitle, width)...)
	return lines
}

func (d *dashboardModel) frameReview(p painter) {
	actions := 0
	for _, item := range d.plan {
		if item.action != actionKeep {
			actions++
		}
	}
	switch {
	case d.check.blocked():
		d.frameReviewBlocked(p, actions)
		return
	case d.check.err != nil:
		p.header("Review · cannot check prerequisites", "nothing has changed")
		lines := wrapped(1, d.check.err.Error(), termui.StyleError, p.w)
		lines = append(lines, nil)
		lines = append(lines, wrapped(1, "skytools checks system packages before changing anything, so this plan cannot start.", termui.StyleNormal, p.w)...)
		p.drawLines("review", lines, 2, p.h-1, &d.check.offset)
		p.hints([2]string{"r", "check again"}, [2]string{"esc", "back"}, [2]string{"q", "quit"})
		return
	}
	noun := "packages"
	if actions == 1 {
		noun = "package"
	}
	p.header(fmt.Sprintf("Review · %d %s", actions, noun), "nothing has changed yet")
	var lines []textLine
	for _, item := range d.plan {
		style := termui.StyleAccentBold
		version := item.to
		switch item.action {
		case actionInstall:
			style = termui.StyleSuccessBold
		case actionRepair:
			style = termui.StyleWarningBold
		case actionKeep:
			style, version = termui.StyleMuted, item.from
		case actionUpdate:
			version = item.from + " → " + item.to
			if item.to == "" {
				version = item.from + " → latest"
			}
		}
		lines = append(lines, textLine{{1, strings.ToUpper(string(item.action)), style}, {10, termui.Truncate(item.pkg.Name, 15), termui.StyleNormal}, {26, termui.Truncate(version, max(1, p.w-27)), termui.StyleNormal}})
		note := ""
		switch {
		case !item.chosen && item.action == actionKeep:
			note = "needed by " + strings.Join(item.neededBy, ", ") + ", already installed, left as it is"
		case !item.chosen:
			note = "needed by " + strings.Join(item.neededBy, ", ")
		case item.action == actionUpdate && item.pkg.Origin == "system":
			note = "new copy in your home; " + d.shorten(item.pkg.Path) + " stays"
		}
		if note != "" {
			lines = append(lines, wrapped(10, note, termui.StyleMuted, p.w)...)
		}
	}
	footer := d.reviewFooter(p.w)
	bottom := p.h - 1
	if p.h >= 14 {
		bottom -= len(footer) + 1
		p.rule(bottom)
		for i, row := range footer {
			for _, run := range row {
				p.put(run.x, bottom+1+i, run.text, run.style)
			}
		}
	} else {
		lines = append(lines, footer...)
	}
	p.drawLines("review", lines, 2, bottom, &d.check.offset)
	if d.toast != "" {
		p.put(1, 1, termui.Truncate(" "+d.toast+" ", p.w-2), termui.StyleWarning)
	}
	if d.check.checking {
		p.hints([2]string{"checking", "prerequisites"}, [2]string{"esc", "back"}, [2]string{"q", "quit"})
	} else {
		p.hints([2]string{"enter", "run"}, [2]string{"esc", "back"}, [2]string{"↑↓", "scroll"}, [2]string{"q", "quit"})
	}
}

func (d *dashboardModel) reviewFooter(width int) []textLine {
	const keyWidth = 15
	prerequisites, style := "✓ all present (read-only check)", termui.StyleSuccess
	if d.check.checking {
		prerequisites, style = spinner()+" checking", termui.StyleMuted
	}
	shell, shellStyle := "✓ hook already in your shell startup files", termui.StyleSuccess
	if len(d.check.unhookedShell) > 0 {
		shell, shellStyle = "adds hook to "+strings.Join(d.check.unhookedShell, ", ")+" (backs each up once)", termui.StyleNormal
	}
	var lines []textLine
	lines = append(lines, keyValue(1, "Prerequisites", prerequisites, style, keyWidth, width)...)
	lines = append(lines, keyValue(1, "Shell", shell, shellStyle, keyWidth, width)...)
	lines = append(lines, keyValue(1, "On failure", "stops; later packages do not start", termui.StyleNormal, keyWidth, width)...)
	return lines
}

func (d *dashboardModel) frameReviewBlocked(p painter, actions int) {
	p.header("Review · blocked", "nothing has changed")
	blocked := d.blockedPlanTools()
	count := len(d.check.report.packageNames())
	noun := "package that is"
	if count != 1 {
		noun = "packages that are"
	}
	verb := "need"
	if len(blocked) == 1 {
		verb = "needs"
	}
	lines := d.prerequisiteLines(fmt.Sprintf("%s %s %d system %s not installed.", strings.Join(blocked, ", "), verb, count, noun), p.w)
	lines = append(lines, nil)
	lines = append(lines, wrapped(1, "As root instead: skytools deps install --only "+strings.Join(blocked, ","), termui.StyleMuted, p.w)...)
	remaining := d.namesWithoutBlocked()
	hints := [][2]string{{"r", "check again"}}
	if len(remaining) > 0 {
		lines = append(lines, nil)
		lines = append(lines, wrapped(1, fmt.Sprintf("The other %d can still run: x drops %s.", len(remaining), strings.Join(blocked, ", ")), termui.StyleNormal, p.w)...)
		hints = append(hints, [2]string{"x", "drop blocked"})
	}
	p.drawLines("review", lines, 2, p.h-1, &d.check.offset)
	p.hints(append(hints, [2]string{"esc", "back"}, [2]string{"q", "quit, print command"})...)
}

func (d *dashboardModel) drawStep(p painter, y int, step *runStep) {
	glyph, glyphStyle, word := "·", termui.StyleMuted, ""
	switch step.phase {
	case phaseQueued:
		word = "queued"
	case phaseRunning:
		glyph, glyphStyle = spinner(), termui.StyleAccent
	case phaseDone:
		glyph, glyphStyle = "✓", termui.StyleSuccess
	case phaseFailed:
		glyph, glyphStyle, word = "✗", termui.StyleErrorBold, "failed"
	case phaseCancelled:
		glyph, glyphStyle, word = "✗", termui.StyleWarning, "cancelled"
	case phaseNotStarted:
		word = "not started"
	}
	nameStyle := termui.StyleNormal
	if step.phase == phaseQueued || step.phase == phaseNotStarted {
		nameStyle = termui.StyleMuted
	}
	p.put(1, y, glyph, glyphStyle)
	p.put(3, y, termui.Truncate(step.name, 15), nameStyle)
	x := 19
	if p.w >= 50 {
		verb := string(step.action)
		p.put(19, y, strings.ToUpper(verb[:1])+verb[1:], termui.StyleMuted)
		x = 27
	}
	elapsed := step.elapsed
	if step.phase == phaseRunning {
		elapsed = time.Since(step.started)
	}
	if word != "" {
		p.put(x, y, word, glyphStyle)
	} else if p.w >= 44 {
		version := step.to
		if step.action == actionUpdate && step.from != "" {
			version = step.from + " → " + step.to
		}
		p.put(x, y, termui.Truncate(version, max(1, p.w-x-8)), termui.StyleMuted)
	}
	if step.phase == phaseRunning || elapsed > 0 {
		p.right(y, clock(elapsed), termui.StyleMuted)
	}
}

func (d *dashboardModel) frameRun(p painter) {
	run := d.run
	done, current := 0, ""
	for _, step := range run.steps {
		switch step.phase {
		case phaseDone:
			done++
		case phaseRunning:
			current = step.name
		}
	}
	title := fmt.Sprintf("Running %d package%s · %d of %d · %s", len(run.steps), plural(len(run.steps)), min(done+1, len(run.steps)), len(run.steps), clock(run.elapsed()))
	if run.self {
		title = "Updating skytools · " + clock(run.elapsed())
	}
	p.put(1, 0, termui.Truncate(title, p.w-2), termui.StyleTitle)
	if run.cancelling {
		p.right(0, "cancelling…", termui.StyleWarning)
	} else if p.w >= 60 && current == "" && done == 0 && !run.self {
		p.right(0, "waiting for the install lock", termui.StyleMuted)
	}
	maxSteps := max(1, min(len(run.steps), p.h*2/5))
	active := 0
	for i, step := range run.steps {
		if step.phase == phaseRunning || step.phase == phaseQueued {
			active = i
			break
		}
		active = i
	}
	from := max(0, min(active-maxSteps/2, len(run.steps)-maxSteps))
	y := 2
	for _, step := range run.steps[from : from+maxSteps] {
		d.drawStep(p, y, step)
		y++
	}
	p.rule(y)
	if current != "" {
		p.put(1, y, " log · now "+current+" ", termui.StyleTitle)
	} else {
		p.put(1, y, " log ", termui.StyleTitle)
	}
	logTop := y + 1
	page := max(1, p.h-1-logTop)
	lines := run.log
	if run.partial != "" {
		lines = append(lines[:len(lines):len(lines)], run.partial)
	}
	if run.follow {
		run.offset = max(0, len(lines)-page)
	}
	p.ui.Scroll("run-log", &run.offset, len(lines), page, !run.confirmQuit)
	run.follow = run.offset >= len(lines)-page
	if run.follow {
		p.right(y, " following ", termui.StyleMuted)
	} else {
		p.right(y, " scrolled, end returns ", termui.StyleMuted)
	}
	for i := 0; i < page && run.offset+i < len(lines); i++ {
		p.put(1, logTop+i, lines[run.offset+i], logStyle(lines[run.offset+i]))
	}
	if run.confirmQuit {
		x := p.put(1, p.h-1, "Cancel the run and quit? ", termui.StyleWarningBold)
		p.put(x, p.h-1, "y yes  n keep running", termui.StyleTitle)
		return
	}
	p.hints([2]string{"↑↓", "scroll log"}, [2]string{"ctrl-c", "cancel"}, [2]string{"q", "cancel and quit"})
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// logStyle dims installer output so skytools' own step lines stand out.
func logStyle(text string) termui.Style {
	if strings.HasPrefix(text, stepOutputIndent) {
		return termui.StyleMuted
	}
	return termui.StyleNormal
}

func (d *dashboardModel) frameResult(p painter) {
	run := d.run
	counts := map[stepAction]int{}
	var failed, cancelled *runStep
	notStarted := 0
	for _, step := range run.steps {
		switch step.phase {
		case phaseDone:
			counts[step.action]++
		case phaseFailed:
			failed = step
		case phaseCancelled:
			cancelled = step
		case phaseNotStarted:
			notStarted++
		}
	}
	var parts []string
	for _, action := range []stepAction{actionUpdate, actionInstall, actionRepair, actionKeep} {
		if counts[action] > 0 {
			word := pastTense[action]
			if action == actionKeep {
				word = "already installed"
			}
			parts = append(parts, fmt.Sprintf("%d %s", counts[action], word))
		}
	}
	if failed != nil {
		parts = append(parts, failed.name+" failed")
	}
	if cancelled != nil {
		parts = append(parts, cancelled.name+" cancelled")
	}
	if notStarted > 0 {
		parts = append(parts, fmt.Sprintf("%d not started", notStarted))
	}
	lead, leadStyle := "✓ Done", termui.StyleSuccessBold
	switch {
	case run.cancelled:
		lead, leadStyle = "Cancelled", termui.StyleWarningBold
	case run.failure != "":
		lead, leadStyle = "✗ Stopped", termui.StyleErrorBold
	}
	x := p.put(1, 0, lead, leadStyle)
	summary := clock(run.elapsed())
	if len(parts) > 0 {
		summary = strings.Join(parts, ", ") + " · " + summary
	}
	p.put(x, 0, termui.Truncate(" · "+summary, p.w-x-1), termui.StyleNormal)
	p.rule(1)
	maxSteps := max(1, min(len(run.steps), (p.h-3)/2))
	y := 2
	for _, step := range run.steps[:maxSteps] {
		d.drawStep(p, y, step)
		y++
	}
	if hidden := len(run.steps) - maxSteps; hidden > 0 {
		p.put(3, y, fmt.Sprintf("and %d more; l opens the full log", hidden), termui.StyleMuted)
		y++
	}
	var lines []textLine
	if run.failure != "" {
		lines = append(lines, wrapped(1, run.failure, termui.StyleError, p.w)...)
	}
	if failed != nil && failed.logFrom < len(run.log) {
		lines = append(lines, nil, line(1, "Last lines from "+failed.name+":", termui.StyleMuted))
		tail := run.log[max(failed.logFrom, len(run.log)-8):]
		for _, text := range tail {
			lines = append(lines, line(3, text, termui.StyleMuted))
		}
	}
	if run.self && counts[actionUpdate] > 0 && d.self.updated != "" {
		lines = append(lines, wrapped(1, "Updated to "+d.self.updated+". The next time you run skytools, it uses the new version.", termui.StyleSuccess, p.w)...)
	} else if run.self && len(run.log) > 0 && run.failure == "" {
		lines = append(lines, wrapped(1, run.log[len(run.log)-1], termui.StyleNormal, p.w)...)
	}
	if !run.self && len(d.finished) > 0 {
		if len(lines) > 0 {
			lines = append(lines, nil)
		}
		lines = append(lines, textLine{{1, "Activate in this shell:", termui.StyleMuted}, {25, `eval "$(skytools env)"`, termui.StyleTitle}})
	}
	p.drawLines("result", lines, y+1, p.h-1, &d.resultOffset)
	hints := [][2]string{{"enter", "packages"}}
	if !run.self && len(d.unfinishedNames()) > 0 {
		hints = append(hints, [2]string{"R", "retry"})
	}
	p.hints(append(hints, [2]string{"l", "full log"}, [2]string{"q", "quit"})...)
}

func (d *dashboardModel) frameLog(p painter) {
	lines := make([]textLine, len(d.run.log))
	for i, text := range d.run.log {
		lines[i] = line(1, text, logStyle(text))
	}
	p.header(fmt.Sprintf("Full log · %d lines", len(lines)), "")
	p.drawLines("log", lines, 2, p.h-1, &d.logOffset)
	p.hints([2]string{"esc", "back"}, [2]string{"↑↓", "scroll"}, [2]string{"pgup pgdn", "page"})
}

func (d *dashboardModel) frameHelp(p painter) {
	p.header("Keys", "")
	keyWidth := 14
	if p.w >= 50 {
		keyWidth = 18
	}
	var lines []textLine
	for _, row := range [][2]string{
		{"↑ ↓  j k", "Move"},
		{"pgup pgdn  g G", "Page, first, last"},
		{"tab shift-tab", "Next or previous view"},
		{"/", "Filter by name"},
		{"space", "Select or clear this row"},
		{"u  m  a", "Select all updates, missing, or visible"},
		{"enter", "Review the selection, or this row"},
		{"→", "Details of this row"},
		{"r", "Rescan and check versions again"},
		{"p", "System prerequisites for all packages"},
		{"S", "Update skytools itself"},
		{"l", "Log of the last run"},
		{"esc", "Back, clear filter, clear selection"},
		{"ctrl-c", "Cancel a run, or quit"},
		{"q", "Quit, asks first during a run"},
	} {
		wrappedRows := keyValue(1, row[0], row[1], termui.StyleNormal, keyWidth, p.w)
		wrappedRows[0][1].style = termui.StyleTitle
		lines = append(lines, wrappedRows...)
	}
	p.drawLines("help", lines, 2, p.h-1, &d.helpOffset)
	p.hints([2]string{"any other key", "back"})
}

func (d *dashboardModel) framePrerequisites(p painter) {
	p.header("System prerequisites", "read-only check, all packages")
	var lines []textLine
	hints := [][2]string{{"esc", "back"}, {"r", "check again"}}
	switch {
	case d.check.checking:
		lines = []textLine{line(1, spinner()+" Checking", termui.StyleMuted)}
	case d.check.err != nil:
		lines = wrapped(1, d.check.err.Error(), termui.StyleWarning, p.w)
	case !d.check.blocked():
		lines = []textLine{line(1, "✓ All required system packages are installed.", termui.StyleSuccess)}
	default:
		count := len(d.check.report.packageNames())
		lines = d.prerequisiteLines(fmt.Sprintf("%d system package%s missing. Needed only by the tools below.", count, plural(count)), p.w)
		lines = append(lines, nil)
		var names []string
		for _, dep := range d.check.report.Missing {
			for _, name := range dep.RequiredBy {
				if !contains(names, name) {
					names = append(names, name)
				}
			}
		}
		lines = append(lines, wrapped(1, "Affected: "+strings.Join(names, ", "), termui.StyleNormal, p.w)...)
		hints = append(hints, [2]string{"q", "quit, print command"})
	}
	p.drawLines("prerequisites", lines, 2, p.h-1, &d.check.offset)
	p.hints(hints...)
}

func (d *dashboardModel) frameSelfUpdate(p painter) {
	p.header("Update skytools", "")
	const keyWidth = 11
	var lines []textLine
	lines = append(lines, keyValue(1, "Running", d.version, termui.StyleNormal, keyWidth, p.w)...)
	switch {
	case d.self.checking:
		lines = append(lines, keyValue(1, "Latest", spinner()+" checking GitHub", termui.StyleMuted, keyWidth, p.w)...)
	case d.self.err != nil:
		lines = append(lines, keyValue(1, "Latest", "check failed: "+d.self.err.Error(), termui.StyleWarning, keyWidth, p.w)...)
	case d.self.latest != "":
		style := termui.StyleNormal
		if d.selfUpdateAvailable() {
			style = termui.StyleAccentBold
		}
		lines = append(lines, keyValue(1, "Latest", d.self.latest+", latest stable GitHub release", style, keyWidth, p.w)...)
	}
	lines = append(lines, nil)
	lines = append(lines, keyValue(1, "Checks", "SHA-256 of the download and the version it reports, then replaces the binary in one step. A failure leaves the old one in place.", termui.StyleNormal, keyWidth, p.w)...)
	lines = append(lines, keyValue(1, "Effect", "Starts with your next skytools run. Packages and shell files are not touched.", termui.StyleNormal, keyWidth, p.w)...)
	p.drawLines("self-update", lines, 2, p.h-1, &d.selfOffset)
	p.hints([2]string{"enter", "update"}, [2]string{"esc", "back"})
}
