package skytools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sky87/skytools/internal/termui"
)

func screenPackagesFixture() []PackageStatus {
	var packages []PackageStatus
	for _, name := range []string{"uv", "rust", "node", "bun", "dotnet", "go", "jdk", "fzf", "gh", "ocaml", "lean", "claude", "codex", "opencode", "pi", "btop", "playwright", "playwright-cli"} {
		packages = append(packages, PackageStatus{Name: name, Label: name + " label", Channel: "stable"})
	}
	return packages
}

// A model at the end of its version checks with one package in every state.
func screenFixture() *dashboardModel {
	packages := screenPackagesFixture()
	set := func(name string, change func(*PackageStatus)) {
		for i := range packages {
			if packages[i].Name == name {
				change(&packages[i])
			}
		}
	}
	installed := func(version, path string) func(*PackageStatus) {
		return func(p *PackageStatus) {
			p.Installed, p.Version, p.Path, p.Origin = true, version, path, "managed"
		}
	}
	for _, name := range []string{"uv", "bun", "go", "jdk", "fzf", "lean", "claude", "btop"} {
		set(name, installed("1.0.0", "/home/dev/.local/bin/"+name))
	}
	set("rust", installed("1.89.0", "/home/dev/.cargo/bin/rustup"))
	set("node", installed("22.0.0", "/home/dev/.local/bin/node"))
	set("gh", func(p *PackageStatus) {
		p.Installed, p.Version, p.Path, p.Origin = true, "2.46.0", "/usr/bin/gh", "system"
	})
	set("opencode", installed("1.0.0", "/home/dev/.opencode/bin/opencode"))
	set("ocaml", func(p *PackageStatus) {
		p.Partial, p.Path, p.Detail = true, "/home/dev/.local/bin/opam", "Incomplete installation; repair required"
	})
	d := newDashboardModel(packages)
	d.version, d.home = "v0.5.0", "/home/dev"
	for _, p := range packages {
		result := dashboardVersionResult{generation: d.versionGeneration, name: p.Name, release: ReleaseInfo{Version: "1.0.0", SourceURL: "https://example.com/" + p.Name}}
		switch p.Name {
		case "rust":
			result.release.Version = "1.91.0"
		case "node":
			result.release.Version = "24.0.0"
		case "gh":
			result.release.Version = "2.82.1"
		case "opencode":
			result.err = errors.New("Get https://example.com: context deadline exceeded")
		}
		d.applyVersion(result)
	}
	return d
}

func drawScreen(d *dashboardModel, width, height int, event termui.Event) string {
	var ui termui.Context
	d.frame(&ui, width, height, event)
	return ui.EndFrame().Text()
}

func press(d *dashboardModel, event termui.Event) dashboardCommand {
	command := d.update(event)
	d.frame(new(termui.Context), d.width, d.height, event)
	return command
}

func runeKey(r rune) termui.Event { return termui.Event{Key: termui.KeyRune, Rune: r} }

func TestPackagesScreenShowsEveryRowAt80By24(t *testing.T) {
	d := screenFixture()
	frame := drawScreen(d, 80, 24, termui.Event{})
	for _, p := range d.packages {
		if !strings.Contains(frame, p.Name) {
			t.Errorf("%s is not visible at 80x24:\n%s", p.Name, frame)
		}
	}
	for _, want := range []string{"skytools v0.5.0", "All 18", "Updates 3", "Missing 5", "Issues 2", "1.89.0", "1.91.0", "↑ update", "· system", "! incomplete", "? check failed", "○ missing", "✓ current", "17/18 checked · 1 failed", "space select"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q:\n%s", want, frame)
		}
	}
	if rows := strings.Split(frame, "\n"); len(rows) != 24 {
		t.Fatalf("frame has %d rows", len(rows))
	}
}

func TestPackagesScreenFitsEverySizeWithoutOverflow(t *testing.T) {
	for _, size := range [][2]int{{36, 10}, {40, 12}, {44, 14}, {60, 18}, {72, 20}, {79, 24}, {80, 24}, {100, 30}, {109, 24}, {110, 24}, {120, 32}, {200, 60}} {
		d := screenFixture()
		d.selected["rust"], d.selected["pi"] = true, true
		for _, screen := range []dashboardScreen{screenPackages, screenDetails, screenHelp, screenSelfUpdate, screenPrerequisites} {
			d.screen = screen
			frame := drawScreen(d, size[0], size[1], termui.Event{})
			rows := strings.Split(frame, "\n")
			if len(rows) != size[1] {
				t.Fatalf("%v screen %d: %d rows", size, screen, len(rows))
			}
			for _, row := range rows {
				if termui.Width(row) >= size[0] {
					t.Fatalf("%v screen %d overflowed the final column: %q", size, screen, row)
				}
			}
		}
	}
}

// The catalog no longer fits in 24 rows: at 80x24 the list scrolls to every
// package, and from 36 rows the whole catalog is on screen at once.
func TestWholeCatalogIsReachableAt80By24AndVisibleAt80By36(t *testing.T) {
	m := testManager(t)
	d := newDashboardModel(m.scan(m.tools()))
	d.version = "test"
	frame := drawScreen(d, 80, 24, termui.Event{})
	if !strings.Contains(frame, "uv: uv (Python manager)") || !strings.Contains(frame, "1-18 of 30") {
		t.Errorf("inspector missing:\n%s", frame)
	}
	for _, p := range d.packages {
		d.cursorName = p.Name
		frame := drawScreen(d, 80, 24, termui.Event{})
		found := false
		for _, row := range strings.Split(frame, "\n") {
			fields := strings.Fields(row)
			found = found || len(fields) > 1 && fields[0] == "›" && fields[1] == p.Name
		}
		if !found {
			t.Errorf("%s is not reachable at 80x24:\n%s", p.Name, frame)
		}
	}
	d.cursorName = "uv"
	frame = drawScreen(d, 80, 36, termui.Event{})
	for _, p := range d.packages {
		if !strings.Contains(frame, p.Name) {
			t.Errorf("%s is not visible at 80x36:\n%s", p.Name, frame)
		}
	}
	rows := append(screenFixture().packages, PackageStatus{Name: "tailscale", Channel: "stable", Installed: true, Path: "/home/dev/.local/bin/tailscale", Origin: "managed"})
	d = newDashboardModel(rows)
	d.home = "/home/dev"
	d.applyVersion(dashboardVersionResult{generation: d.versionGeneration, name: "uv", release: ReleaseInfo{Version: "2.0.0"}})
	d.cursorName = "tailscale"
	if frame := drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "tailscale · ~/.local/bin/tailscale · ") {
		t.Errorf("compact inspector lost the path:\n%s", frame)
	}
}

func TestEveryWidthCanSeeHowToQuitAndGetHelp(t *testing.T) {
	for _, width := range []int{36, 40, 54, 56, 60, 72, 80, 88, 120} {
		frame := drawScreen(screenFixture(), width, 24, termui.Event{})
		rows := strings.Split(frame, "\n")
		if bar := rows[len(rows)-1]; !strings.Contains(bar, "q quit") || !strings.Contains(bar, "? help") {
			t.Errorf("width %d key bar: %q", width, bar)
		}
	}
}

func TestFocusedRowIsOneSolidBar(t *testing.T) {
	var ui termui.Context
	d := screenFixture()
	d.cursorName = "rust"
	d.frame(&ui, 80, 24, termui.Event{})
	raw := fmt.Sprintf("%#v", ui.EndFrame())
	found := false
	for _, row := range strings.Split(raw, `", "`) {
		if strings.Contains(row, "›") {
			found = true
			if !strings.HasPrefix(row, `\x1b[0;7m`) || strings.Count(row, `\x1b[`) != 1 {
				t.Fatalf("focused row changes style partway: %s", row)
			}
		}
	}
	if !found {
		t.Fatalf("focused row missing: %s", raw)
	}
}

func TestUpdateKeySelectsOutdatedAndReviewShowsThePlan(t *testing.T) {
	d := screenFixture()
	press(d, runeKey('u'))
	if got := strings.Join(d.names(), ","); got != "rust,node,gh" {
		t.Fatalf("selection = %s", got)
	}
	if frame := drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "3 selected: rust, node, gh") {
		t.Fatalf("selection not shown:\n%s", frame)
	}
	if command := press(d, termui.Event{Key: termui.KeyEnter}); command != commandCheckPlan || d.screen != screenReview {
		t.Fatalf("enter = %v on screen %d", command, d.screen)
	}
	if command := press(d, termui.Event{Key: termui.KeyEnter}); command != commandNone {
		t.Fatal("Run started before prerequisites were checked")
	}
	d.applyPrerequisites(prerequisiteResult{generation: d.check.generation})
	frame := drawScreen(d, 80, 24, termui.Event{})
	for _, want := range []string{"Review · 3 packages", "nothing has changed yet", "UPDATE", "1.89.0 → 1.91.0", "new copy in your home; /usr/bin/gh stays", "✓ all present", "stops; later packages do not start"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q:\n%s", want, frame)
		}
	}
	if command := press(d, termui.Event{Key: termui.KeyEnter}); command != commandRun {
		t.Fatalf("second enter = %v", command)
	}
}

func TestPlanKeepsInstalledDependencyUnlessSelected(t *testing.T) {
	d := screenFixture()
	plan := d.buildPlan([]string{"pi"})
	if len(plan) != 2 || plan[0].pkg.Name != "node" || plan[0].action != actionKeep || plan[0].command() != "ensure" || plan[0].chosen {
		t.Fatalf("dependency = %+v", plan)
	}
	if plan[1].pkg.Name != "pi" || plan[1].action != actionInstall {
		t.Fatalf("selected = %+v", plan[1])
	}
	plan = d.buildPlan([]string{"node", "pi"})
	if plan[0].action != actionUpdate || plan[0].command() != "update" {
		t.Fatalf("selected dependency = %+v", plan[0])
	}
	plan = d.buildPlan([]string{"playwright-cli"})
	var names []string
	for _, item := range plan {
		names = append(names, item.pkg.Name+":"+string(item.action))
	}
	if got := strings.Join(names, ","); got != "node:keep,playwright:install,playwright-cli:install" {
		t.Fatalf("transitive plan = %s", got)
	}
	if plan := d.buildPlan([]string{"ocaml"}); plan[0].action != actionRepair {
		t.Fatalf("partial package = %+v", plan[0])
	}
	d.openReview([]string{"pi"})
	d.applyPrerequisites(prerequisiteResult{generation: d.check.generation})
	frame := drawScreen(d, 80, 24, termui.Event{})
	for _, want := range []string{"Review · 1 package", "KEEP", "needed by pi, already installed, left as it is", "INSTALL"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q:\n%s", want, frame)
		}
	}
}

func blockedReport() SystemDependencyReport {
	return SystemDependencyReport{
		Platform:       "Debian/dpkg",
		InstallCommand: "sudo apt-get install --no-install-recommends bubblewrap libgmp-dev m4 rsync",
		Missing: []MissingSystemDependency{
			{"file-sync", []string{"rsync"}, []string{"ocaml"}}, {"gmp-dev", []string{"libgmp-dev"}, []string{"ocaml"}},
			{"m4", []string{"m4"}, []string{"ocaml"}}, {"process-sandbox", []string{"bubblewrap"}, []string{"ocaml"}},
		},
	}
}

func TestBlockedReviewShowsWholeCommandAndCanDropTheBlocker(t *testing.T) {
	for _, width := range []int{80, 60, 40} {
		d := screenFixture()
		d.selected["rust"], d.selected["ocaml"] = true, true
		d.width, d.height = width, 24
		d.openReview(d.names())
		d.applyPrerequisites(prerequisiteResult{generation: d.check.generation, report: blockedReport()})
		frame := drawScreen(d, width, 24, termui.Event{})
		joined := strings.Join(strings.Fields(frame), " ")
		if !strings.Contains(joined, "ocaml needs 4 system packages") {
			t.Errorf("width %d intro:\n%s", width, frame)
		}
		if !strings.Contains(joined, blockedReport().InstallCommand) {
			t.Errorf("width %d lost the apt command:\n%s", width, frame)
		}
		if command := d.update(termui.Event{Key: termui.KeyEnter}); command != commandNone {
			t.Fatal("a blocked plan can run")
		}
		if command := d.update(runeKey('x')); command != commandCheckPlan {
			t.Fatalf("x = %v", command)
		}
		if got := strings.Join(d.planNames, ","); got != "rust" || d.selected["ocaml"] {
			t.Fatalf("after x: plan %s, selected %v", got, d.selected)
		}
	}
	d := screenFixture()
	d.openReview([]string{"ocaml"})
	d.applyPrerequisites(prerequisiteResult{generation: d.check.generation, report: blockedReport()})
	if command := d.update(runeKey('q')); command != commandQuit || !d.printMissing {
		t.Fatalf("q = %v, printMissing = %v", command, d.printMissing)
	}
	receipt := strings.Join(d.receipt(), "\n")
	if !strings.Contains(receipt, blockedReport().InstallCommand) {
		t.Fatalf("receipt: %s", receipt)
	}
}

func TestBlockedReviewOffersToInstallWithSudo(t *testing.T) {
	report := blockedReport()
	report.InstallSteps = [][]string{{"/usr/bin/apt-get", "update"}, {"/usr/bin/apt-get", "install", "--no-install-recommends", "libgmp-dev"}}
	for _, tc := range []struct {
		name       string
		prefix     []string
		canInstall bool
		want       []string
		hint       string
	}{
		{"sudo", []string{"/usr/bin/sudo"}, true, []string{"Press i to run these now", "sudo apt-get update", "sudo apt-get install --no-install-recommends libgmp-dev", "sudo asks for your password"}, "i install with sudo"},
		{"root", nil, true, []string{"Press i to run these now", "apt-get update", "apt-get install --no-install-recommends libgmp-dev"}, "i install"},
		{"neither", nil, false, []string{"Run this in another shell, then press r:", report.InstallCommand}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := screenFixture()
			d.installPrefix, d.canInstall = tc.prefix, tc.canInstall
			d.openReview([]string{"ocaml"})
			d.applyPrerequisites(prerequisiteResult{generation: d.check.generation, report: report})
			frame := drawScreen(d, 100, 30, termui.Event{})
			joined := strings.Join(strings.Fields(frame), " ")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("missing %q:\n%s", want, frame)
				}
			}
			if tc.name == "root" && strings.Contains(joined, "sudo") {
				t.Errorf("root is shown sudo:\n%s", frame)
			}
			command := d.update(runeKey('i'))
			if !tc.canInstall {
				if command != commandNone || strings.Contains(joined, "i install") {
					t.Fatalf("i offered without sudo or root: %v\n%s", command, frame)
				}
				return
			}
			if !strings.Contains(joined, tc.hint) || command != commandInstallSystem {
				t.Fatalf("i = %v, hint %q:\n%s", command, tc.hint, frame)
			}
			if command := d.systemInstalled(nil); command != commandCheckPlan || !d.check.checking {
				t.Fatalf("after install: %v, checking %v", command, d.check.checking)
			}
		})
	}
	// Nothing is missing: i does nothing.
	d := screenFixture()
	d.installPrefix, d.canInstall = []string{"/usr/bin/sudo"}, true
	d.openReview([]string{"uv"})
	d.applyPrerequisites(prerequisiteResult{generation: d.check.generation})
	if command := d.update(runeKey('i')); command != commandNone {
		t.Fatalf("i with nothing missing = %v", command)
	}
	// From the all-packages prerequisites screen it checks everything again.
	d = screenFixture()
	d.installPrefix, d.canInstall = []string{"/usr/bin/sudo"}, true
	d.update(runeKey('p'))
	d.applyPrerequisites(prerequisiteResult{generation: d.check.generation, report: report})
	if command := d.update(runeKey('i')); command != commandInstallSystem {
		t.Fatalf("prerequisites i = %v", command)
	}
	if command := d.systemInstalled(errors.New("exit status 100")); command != commandCheckAll || !strings.Contains(d.toast, "exit status 100") {
		t.Fatalf("after failed install: %v, toast %q", command, d.toast)
	}
}

func TestStaleBackgroundPrerequisiteResultIsIgnored(t *testing.T) {
	d := screenFixture()
	d.openReview([]string{"uv"})
	stale := d.check.generation
	d.openReview([]string{"ocaml"})
	d.applyPrerequisites(prerequisiteResult{generation: stale, err: errors.New("old")})
	if !d.check.checking || d.check.err != nil {
		t.Fatalf("stale result applied: %+v", d.check)
	}
}

func TestSpaceSelectsAnyRowAndEnterActsWithoutASelection(t *testing.T) {
	d := screenFixture()
	press(d, termui.Event{Key: termui.KeySpace})
	if !d.selected["uv"] {
		t.Fatal("space ignored a current package")
	}
	press(d, termui.Event{Key: termui.KeyEscape})
	if len(d.names()) != 0 {
		t.Fatal("esc did not clear the selection")
	}
	if command := press(d, termui.Event{Key: termui.KeyEnter}); command != commandNone || !strings.Contains(d.toast, "uv is current") {
		t.Fatalf("enter on current row: %v %q", command, d.toast)
	}
	press(d, termui.Event{Key: termui.KeyDown})
	if command := press(d, termui.Event{Key: termui.KeyEnter}); command != commandCheckPlan || strings.Join(d.planNames, ",") != "rust" {
		t.Fatalf("enter on outdated row: %v %v", command, d.planNames)
	}
}

func TestViewsAndFindFilterRowsAndKeepTheCursorInRange(t *testing.T) {
	d := screenFixture()
	press(d, termui.Event{Key: termui.KeyEnd})
	press(d, termui.Event{Key: termui.KeyTab})
	if rows := d.visible(); d.filter != filterUpdates || len(rows) != 3 || d.cursor != 0 {
		t.Fatalf("updates view: filter %d, %d rows, cursor %d", d.filter, len(rows), d.cursor)
	}
	press(d, termui.Event{Key: termui.KeyTab})
	press(d, termui.Event{Key: termui.KeyTab})
	if rows := d.visible(); len(rows) != 2 || rows[0].Name != "ocaml" || rows[1].Name != "opencode" {
		t.Fatalf("issues view: %+v", rows)
	}
	press(d, termui.Event{Key: termui.KeyBackTab})
	press(d, termui.Event{Key: termui.KeyBackTab})
	press(d, termui.Event{Key: termui.KeyBackTab})
	press(d, runeKey('/'))
	for _, r := range "playx" {
		press(d, runeKey(r))
	}
	press(d, termui.Event{Key: termui.KeyBackspace})
	if rows := d.visible(); !d.finding || len(rows) != 2 {
		t.Fatalf("find: finding %v, rows %+v", d.finding, rows)
	}
	press(d, termui.Event{Key: termui.KeyEnter})
	if frame := drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "/ play") {
		t.Fatalf("filter not shown:\n%s", frame)
	}
	press(d, termui.Event{Key: termui.KeyEscape})
	if d.find != "" || len(d.visible()) != 18 {
		t.Fatal("esc did not clear the filter")
	}
}

func TestDetailsShowFullErrorsAndCommandLine(t *testing.T) {
	d := screenFixture()
	d.cursorName = "opencode"
	drawScreen(d, 80, 24, termui.Event{})
	press(d, termui.Event{Key: termui.KeyRight})
	frame := drawScreen(d, 80, 24, termui.Event{})
	for _, want := range []string{"opencode", "? check failed", "context deadline exceeded", "~/.opencode/bin/opencode", "skytools update --only opencode"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q:\n%s", want, frame)
		}
	}
	press(d, termui.Event{Key: termui.KeyDown})
	if p, _ := d.focusedPackage(); p.Name != "pi" {
		t.Fatalf("down moved to %s", p.Name)
	}
	if frame := drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "Needed by") || !strings.Contains(frame, "Requires       node") {
		t.Fatalf("dependencies missing:\n%s", frame)
	}
	press(d, termui.Event{Key: termui.KeyEscape})
	if d.screen != screenPackages {
		t.Fatal("esc did not go back")
	}
}

func TestReadOnlyPlatformExplainsAndRefusesSelection(t *testing.T) {
	d := screenFixture()
	d.readOnly = "Install and Update need Debian-family Linux. Browsing works."
	press(d, termui.Event{Key: termui.KeySpace})
	press(d, runeKey('u'))
	if command := press(d, termui.Event{Key: termui.KeyEnter}); command != commandNone || len(d.names()) != 0 {
		t.Fatalf("read-only platform accepted a plan: %v %v", command, d.names())
	}
	if frame := drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "need Debian-family Linux") || strings.Contains(frame, "space select") {
		t.Fatalf("frame:\n%s", frame)
	}
	if press(d, runeKey('s')); d.screen != screenSelfUpdate {
		t.Fatal("self-update does not depend on the system package provider")
	}
}

func runningFixture(t *testing.T) (*dashboardModel, *dashboardJob, chan error) {
	t.Helper()
	d := screenFixture()
	d.planNames = []string{"rust", "gh", "pi"}
	d.plan = d.buildPlan(d.planNames)
	d.beginPlanRun()
	release := make(chan error)
	job := startDashboardJob(context.Background(), func(ctx context.Context, output io.Writer, report func(stepEvent)) error {
		return <-release
	})
	t.Cleanup(func() {
		job.cancel()
		select {
		case release <- nil:
		default:
		}
	})
	return d, job, release
}

func TestRunScreenHidesKeptDependencyAndShowsProgress(t *testing.T) {
	d, job, _ := runningFixture(t)
	if len(d.run.steps) != 3 {
		t.Fatalf("steps = %d", len(d.run.steps))
	}
	job.log.report(stepEvent{Phase: stepStarted, Name: "rust", Action: actionUpdate})
	for i := 0; i < 30; i++ {
		fmt.Fprintf(job.log, "output line %d\n", i)
	}
	d.syncRun(job)
	frame := drawScreen(d, 80, 24, termui.Event{})
	for _, want := range []string{"Running 3 packages · 1 of 3", "1.89.0 → 1.91.0", "queued", "log · now rust", "following", "output line 29", "ctrl-c"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "node") {
		t.Errorf("kept dependency shown as work:\n%s", frame)
	}
	logRows := 0
	for _, row := range strings.Split(frame, "\n") {
		if strings.Contains(row, "output line") {
			logRows++
		}
	}
	if logRows != 17 {
		t.Errorf("log rows at 80x24 with 3 steps = %d", logRows)
	}
	drawScreen(d, 80, 24, termui.Event{Key: termui.KeyUp})
	frame = drawScreen(d, 80, 24, termui.Event{})
	if !strings.Contains(frame, "scrolled") || strings.Contains(frame, "output line 29") {
		t.Errorf("scrolling up kept following:\n%s", frame)
	}
	fmt.Fprintln(job.log, "newest")
	d.syncRun(job)
	if frame = drawScreen(d, 80, 24, termui.Event{}); strings.Contains(frame, "newest") {
		t.Errorf("new output moved a scrolled log:\n%s", frame)
	}
	drawScreen(d, 80, 24, termui.Event{Key: termui.KeyEnd})
	if frame = drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "newest") || !strings.Contains(frame, "following") {
		t.Errorf("End did not return to the live log:\n%s", frame)
	}
}

func TestRunQuitAsksFirstAndCtrlCCancelsAtOnce(t *testing.T) {
	d, _, _ := runningFixture(t)
	if command := d.update(runeKey('q')); command != commandNone || !d.run.confirmQuit {
		t.Fatalf("q = %v", command)
	}
	if frame := drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "Cancel the run and quit?") {
		t.Fatalf("no prompt:\n%s", frame)
	}
	if command := d.update(runeKey('n')); command != commandNone || d.run.confirmQuit {
		t.Fatal("n did not keep running")
	}
	d.update(runeKey('q'))
	if command := d.update(runeKey('y')); command != commandCancelAndQuit {
		t.Fatalf("y = %v", command)
	}
	d, _, _ = runningFixture(t)
	if command := d.update(termui.Event{Key: termui.KeyCtrlC}); command != commandCancel || !d.run.cancelling {
		t.Fatalf("ctrl-c = %v", command)
	}
}

func TestResultSeparatesFinishedFailedAndNotStarted(t *testing.T) {
	d, job, _ := runningFixture(t)
	job.log.report(stepEvent{Phase: stepStarted, Name: "rust", Action: actionUpdate})
	job.log.report(stepEvent{Phase: stepDone, Name: "rust", Action: actionUpdate, Elapsed: 21 * time.Second})
	job.log.report(stepEvent{Phase: stepStarted, Name: "gh", Action: actionUpdate})
	fmt.Fprintln(job.log, "download gh")
	fmt.Fprintln(job.log, "verify gh")
	failure := errors.New("gh: verify gh_2.82.1_linux_arm64.tar.gz: SHA-256 mismatch")
	job.log.report(stepEvent{Phase: stepFailed, Name: "gh", Action: actionUpdate, Err: failure})
	packages := append([]PackageStatus{}, d.packages...)
	for i := range packages {
		if packages[i].Name == "rust" {
			packages[i].Version = "1.91.0"
		}
	}
	gh := d.versions["gh"].latest
	d.selected["rust"], d.selected["gh"] = true, true
	d.completeRun(job, failure, packages)
	frame := drawScreen(d, 80, 24, termui.Event{})
	for _, want := range []string{"✗ Stopped", "1 updated", "gh failed", "1 not started", "SHA-256 mismatch", "Last lines from gh", "verify gh", `eval "$(skytools env)"`, "retry", "full log"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q:\n%s", want, frame)
		}
	}
	if d.selected["rust"] || !d.selected["gh"] {
		t.Fatalf("selection after run: %v", d.selected)
	}
	if d.versions["gh"].latest != gh || d.versions["gh"].latestChecking {
		t.Fatal("finishing a run threw away known upstream versions")
	}
	if tasks := d.pendingVersionTasks(); len(tasks) != 0 {
		t.Fatalf("run refetched %d versions", len(tasks))
	}
	receipt := strings.Join(d.receipt(), "\n")
	for _, want := range []string{"skytools: updated rust 1.91.0", "SHA-256 mismatch", "not started: pi", `eval "$(skytools env)"`} {
		if !strings.Contains(receipt, want) {
			t.Errorf("receipt missing %q:\n%s", want, receipt)
		}
	}
	if command := d.update(runeKey('R')); command != commandRun || strings.Join(d.planNames, ",") != "gh,pi" {
		t.Fatalf("retry = %v %v", command, d.planNames)
	}
}

func TestCancelledRunIsNotReportedAsFailure(t *testing.T) {
	d, job, _ := runningFixture(t)
	job.log.report(stepEvent{Phase: stepStarted, Name: "rust", Action: actionUpdate})
	job.log.report(stepEvent{Phase: stepFailed, Name: "rust", Action: actionUpdate, Err: errors.New("signal: killed")})
	d.completeRun(job, context.Canceled, d.packages)
	frame := drawScreen(d, 80, 24, termui.Event{})
	if !strings.Contains(frame, "Cancelled") || strings.Contains(frame, "failed") || strings.Contains(frame, "signal: killed") {
		t.Fatalf("frame:\n%s", frame)
	}
	if receipt := strings.Join(d.receipt(), "\n"); !strings.Contains(receipt, "cancelled during rust") || strings.Contains(receipt, "activate") {
		t.Fatalf("receipt:\n%s", receipt)
	}
}

func TestFullLogOpensAtTheEndAndKeepsEveryLine(t *testing.T) {
	d, job, _ := runningFixture(t)
	for i := 0; i < 500; i++ {
		fmt.Fprintf(job.log, "line %d\n", i)
	}
	d.completeRun(job, nil, d.packages)
	press(d, runeKey('l'))
	frame := drawScreen(d, 80, 24, termui.Event{})
	if !strings.Contains(frame, "500 lines") || !strings.Contains(frame, "line 499") {
		t.Fatalf("frame:\n%s", frame)
	}
	drawScreen(d, 80, 24, termui.Event{Key: termui.KeyHome})
	if frame = drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "line 0") {
		t.Fatalf("first line lost:\n%s", frame)
	}
	press(d, termui.Event{Key: termui.KeyEscape})
	if d.screen != screenResult {
		t.Fatal("esc did not return to the result")
	}
}

func TestFullLogWrapsLongLinesAndRunScreenMarksThem(t *testing.T) {
	const long = "      ==> Installing standalone package to /home/dev/.codex/packages/standalone/releases/0.166.0-aarch64-unknown-linux-gnu"
	d, job, _ := runningFixture(t)
	fmt.Fprintln(job.log, long)
	d.syncRun(job)
	if frame := drawScreen(d, 80, 24, termui.Event{}); !strings.Contains(frame, "…") {
		t.Errorf("run screen cut a line without marking it:\n%s", frame)
	}
	d.completeRun(job, nil, d.packages)
	press(d, runeKey('l'))
	frame := drawScreen(d, 80, 24, termui.Event{})
	joined := strings.Join(strings.Fields(frame), "")
	if !strings.Contains(joined, strings.ReplaceAll(long, " ", "")) {
		t.Fatalf("full log lost text:\n%s", frame)
	}
	lines := wrapLogLine(long, 40)
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "        ") {
		t.Fatalf("continuation lost its indent: %q", lines)
	}
	for _, part := range lines {
		if termui.Width(part) > 40 {
			t.Fatalf("overflowed: %q", part)
		}
	}
	if got := wrapLogLine("a  b", 40); len(got) != 1 || got[0] != "a  b" {
		t.Fatalf("short line changed: %q", got)
	}
}

func TestReceiptIsEmptyAfterOnlyBrowsing(t *testing.T) {
	if receipt := screenFixture().receipt(); len(receipt) != 0 {
		t.Fatalf("receipt = %v", receipt)
	}
}

func TestSelfUpdateIsOnlyOfferedForAKnownNewerRelease(t *testing.T) {
	d := screenFixture()
	if strings.Contains(drawScreen(d, 80, 24, termui.Event{}), "available (S)") {
		t.Fatal("offered before a check finished")
	}
	d.applyVersion(dashboardVersionResult{generation: d.versionGeneration, self: true, version: "v0.6.0"})
	if !strings.Contains(drawScreen(d, 80, 24, termui.Event{}), "v0.6.0 available (S)") {
		t.Fatal("newer release not offered")
	}
	d.version = "v0.6.0"
	if strings.Contains(drawScreen(d, 80, 24, termui.Event{}), "available (S)") {
		t.Fatal("offered the running version")
	}
	d.version = "dev"
	if strings.Contains(drawScreen(d, 80, 24, termui.Event{}), "available (S)") {
		t.Fatal("offered an unknown comparison")
	}
	d.version = "v0.5.0"
	if press(d, runeKey('S')); d.screen != screenSelfUpdate {
		t.Fatal("S did not open self-update")
	}
	frame := strings.Join(strings.Fields(drawScreen(d, 80, 24, termui.Event{})), " ")
	for _, want := range []string{"Update skytools", "v0.5.0", "v0.6.0", "SHA-256", "next skytools run", "Packages and shell files are not touched"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q:\n%s", want, frame)
		}
	}
	if command := d.update(termui.Event{Key: termui.KeyEnter}); command != commandSelfUpdate {
		t.Fatalf("enter = %v", command)
	}
}

func TestSmallTerminalOnlyAcceptsQuit(t *testing.T) {
	d := screenFixture()
	d.width, d.height = 30, 8
	if frame := drawScreen(d, 30, 8, termui.Event{}); !strings.Contains(frame, "Terminal too small") {
		t.Fatalf("frame:\n%s", frame)
	}
	if command := d.update(termui.Event{Key: termui.KeyEnter}); command != commandNone || d.screen != screenPackages {
		t.Fatal("small terminal activated an invisible control")
	}
	if command := d.update(runeKey('q')); command != commandQuit {
		t.Fatal("q does not quit a small terminal")
	}
	drawScreen(d, 12, 4, termui.Event{Key: termui.KeyEnter})
}

func TestProfilesWithoutHookOnlyReads(t *testing.T) {
	m := testManager(t)
	missing := m.profilesWithoutHook()
	if len(missing) != 3 || missing[0] != "~/.profile" {
		t.Fatalf("missing = %v", missing)
	}
	if exists(m.at(".profile")) {
		t.Fatal("checking the hook wrote a file")
	}
	if err := m.setupShell(); err != nil {
		t.Fatal(err)
	}
	if missing := m.profilesWithoutHook(); len(missing) != 0 {
		t.Fatalf("after setup, missing = %v", missing)
	}
}
