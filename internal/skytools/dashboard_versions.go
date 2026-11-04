package skytools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sky87/skytools/internal/termui"
)

const dashboardVersionWorkers = 4
const dashboardVersionTimeout = 10 * time.Second

type dashboardVersionProbes struct {
	installed func(context.Context, PackageStatus) (string, error)
	latest    func(context.Context, string) (ReleaseInfo, error)
}

type dashboardPackageVersions struct {
	installed, latest, channel, source string
	installedChecking, latestChecking  bool
	installedErr, latestErr            error
}

type dashboardVersionResult struct {
	generation uint64
	name       string
	installed  bool
	version    string
	release    ReleaseInfo
	err        error
}

type dashboardVersionCheck struct {
	cancel  context.CancelFunc
	results chan dashboardVersionResult
	done    chan struct{}
}

func (m *manager) versionProbes() dashboardVersionProbes {
	return dashboardVersionProbes{
		installed: m.installedVersion,
		latest: func(ctx context.Context, name string) (ReleaseInfo, error) {
			worker := *m
			worker.ctx, worker.out, worker.errOut = ctx, io.Discard, io.Discard
			worker.env = append([]string{}, m.env...)
			return worker.latestVersion(name)
		},
	}
}

// startDashboardVersions never touches UI state. Workers receive immutable
// inventory values and return generation-tagged results to the UI goroutine.
func startDashboardVersions(parent context.Context, generation uint64, packages []PackageStatus, probes dashboardVersionProbes) *dashboardVersionCheck {
	ctx, cancel := context.WithCancel(parent)
	type task struct {
		p         PackageStatus
		installed bool
	}
	var tasks []task
	for _, p := range packages {
		if p.Installed && p.Version == "" {
			tasks = append(tasks, task{p: p, installed: true})
		}
	}
	for _, p := range packages {
		tasks = append(tasks, task{p: p})
	}
	check := &dashboardVersionCheck{cancel: cancel, results: make(chan dashboardVersionResult, len(tasks)), done: make(chan struct{})}
	queue := make(chan task, len(tasks))
	for _, task := range tasks {
		queue <- task
	}
	close(queue)
	var workers sync.WaitGroup
	for i := 0; i < min(dashboardVersionWorkers, len(tasks)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range queue {
				if ctx.Err() != nil {
					return
				}
				taskCtx, stop := context.WithTimeout(ctx, dashboardVersionTimeout)
				result := dashboardVersionResult{generation: generation, name: task.p.Name, installed: task.installed}
				if task.installed {
					result.version, result.err = probes.installed(taskCtx, task.p)
				} else {
					result.release, result.err = probes.latest(taskCtx, task.p.Name)
				}
				if taskCtx.Err() != nil {
					result.err = taskCtx.Err()
				}
				stop()
				select {
				case check.results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(check.results)
		close(check.done)
		cancel()
	}()
	return check
}

func (d *dashboardModel) resetVersions() {
	d.versionGeneration++
	d.versions = make(map[string]dashboardPackageVersions, len(d.packages))
	for _, p := range d.packages {
		d.versions[p.Name] = dashboardPackageVersions{
			installed: p.Version, channel: p.Channel,
			installedChecking: p.Installed && p.Version == "",
			latestChecking:    true,
		}
	}
}

func (d *dashboardModel) applyVersion(result dashboardVersionResult) {
	if result.generation != d.versionGeneration {
		return
	}
	v, ok := d.versions[result.name]
	if !ok {
		return
	}
	if result.installed {
		v.installed, v.installedErr, v.installedChecking = result.version, result.err, false
	} else {
		v.latest, v.source, v.latestErr, v.latestChecking = result.release.Version, result.release.SourceURL, result.err, false
		if result.release.Channel != "" {
			v.channel = result.release.Channel
		}
	}
	d.versions[result.name] = v
}

func versionFailure(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var network net.Error
	if errors.As(err, &network) {
		return "offline"
	}
	return "error"
}

func (d *dashboardModel) versionCells(p PackageStatus) (installed, latest, channel, status string, style termui.Style) {
	v := d.versions[p.Name]
	channel = v.channel
	if channel == "" {
		channel = p.Channel
	}
	installed, latest, status, style = v.installed, v.latest, "Unknown", termui.StyleMuted
	if !p.Installed && !p.Partial {
		installed = "missing"
	} else if v.installedChecking {
		installed = "checking"
	} else if v.installedErr != nil || installed == "" {
		installed = versionFailure(v.installedErr)
	}
	if v.latestChecking {
		latest = "checking"
	} else if v.latestErr != nil || latest == "" {
		latest = versionFailure(v.latestErr)
	}
	switch {
	case !p.Installed && !p.Partial:
		status = "Missing"
	case p.Partial:
		status, style = "Incomplete", termui.StyleError
	case v.installedChecking || v.latestChecking:
		status = "Checking"
	case v.installedErr != nil || v.latestErr != nil:
		status = "Unknown"
	default:
		if comparison, ok := compareVersions(v.installed, v.latest); ok {
			switch comparison {
			case -1:
				status, style = "Update available", termui.StyleAccent
			case 0:
				status, style = "Current", termui.StyleSuccess
			case 1:
				status, style = "Newer than target", termui.StyleMuted
			}
		}
	}
	return
}

func (d *dashboardModel) versionDetail(p PackageStatus) string {
	v := d.versions[p.Name]
	installed, latest, channel, status, _ := d.versionCells(p)
	parts := []string{fmt.Sprintf("Installed: %s | Latest %s: %s | %s", installed, channel, latest, status)}
	if v.installedErr != nil {
		parts = append(parts, "installed check: "+v.installedErr.Error())
	}
	if v.latestErr != nil {
		parts = append(parts, "latest check: "+v.latestErr.Error())
	}
	if status == "Newer than target" {
		parts = append(parts, "Different target: installed version is newer than the requested "+channel+" release")
	}
	return strings.Join(parts, " | ")
}

func versionColumn(value string, width int) string {
	value = termui.Sanitize(value)
	if utf8.RuneCountInString(value) <= width {
		return value
	}
	return string([]rune(value)[:width-1]) + "…"
}
