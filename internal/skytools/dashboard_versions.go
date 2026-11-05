package skytools

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const dashboardVersionWorkers = 4
const dashboardVersionTimeout = 10 * time.Second

type dashboardVersionProbes struct {
	installed func(context.Context, PackageStatus) (string, error)
	latest    func(context.Context, string) (ReleaseInfo, error)
	self      func(context.Context) (string, error)
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
	self       bool
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
		self: func(ctx context.Context) (string, error) {
			worker := *m
			worker.ctx, worker.out, worker.errOut = ctx, io.Discard, io.Discard
			worker.env = append([]string{}, m.env...)
			release, err := worker.githubRelease(selfRepository)
			return release.Tag, err
		},
	}
}

type dashboardVersionTask struct {
	p               PackageStatus
	installed, self bool
}

func allVersionTasks(packages []PackageStatus) []dashboardVersionTask {
	var tasks []dashboardVersionTask
	for _, p := range packages {
		if p.Installed && p.Version == "" {
			tasks = append(tasks, dashboardVersionTask{p: p, installed: true})
		}
	}
	for _, p := range packages {
		tasks = append(tasks, dashboardVersionTask{p: p})
	}
	return tasks
}

// pendingVersionTasks asks only for what is still unknown, so finishing a run
// does not fetch every upstream version again.
func (d *dashboardModel) pendingVersionTasks() []dashboardVersionTask {
	var tasks []dashboardVersionTask
	for _, p := range d.packages {
		if d.versions[p.Name].installedChecking {
			tasks = append(tasks, dashboardVersionTask{p: p, installed: true})
		}
	}
	for _, p := range d.packages {
		if d.versions[p.Name].latestChecking {
			tasks = append(tasks, dashboardVersionTask{p: p})
		}
	}
	if d.self.checking {
		tasks = append(tasks, dashboardVersionTask{self: true})
	}
	return tasks
}

func startDashboardVersions(parent context.Context, generation uint64, packages []PackageStatus, probes dashboardVersionProbes) *dashboardVersionCheck {
	return startDashboardVersionTasks(parent, generation, allVersionTasks(packages), probes)
}

// startDashboardVersionTasks never touches UI state. Workers receive immutable
// inventory values and return generation-tagged results to the UI goroutine.
func startDashboardVersionTasks(parent context.Context, generation uint64, tasks []dashboardVersionTask, probes dashboardVersionProbes) *dashboardVersionCheck {
	ctx, cancel := context.WithCancel(parent)
	check := &dashboardVersionCheck{cancel: cancel, results: make(chan dashboardVersionResult, len(tasks)), done: make(chan struct{})}
	queue := make(chan dashboardVersionTask, len(tasks))
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
				result := dashboardVersionResult{generation: generation, name: task.p.Name, installed: task.installed, self: task.self}
				switch {
				case task.self:
					result.version, result.err = probes.self(taskCtx)
				case task.installed:
					result.version, result.err = probes.installed(taskCtx, task.p)
				default:
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
	d.self.checking, d.self.err = d.self.probe, nil
}

// resetInstalledVersion re-reads one package after a run touched it. The latest
// version upstream has not changed because of that, so it is kept.
func (d *dashboardModel) resetInstalledVersion(p PackageStatus) {
	v := d.versions[p.Name]
	v.installed, v.installedErr = p.Version, nil
	v.installedChecking = p.Installed && p.Version == ""
	d.versions[p.Name] = v
}

func (d *dashboardModel) applyVersion(result dashboardVersionResult) {
	if result.generation != d.versionGeneration {
		return
	}
	if result.self {
		d.self.latest, d.self.err, d.self.checking = result.version, result.err, false
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

func (d *dashboardModel) selfUpdateAvailable() bool {
	if d.self.latest == "" || d.self.err != nil || d.self.updated != "" {
		return false
	}
	comparison, known := compareVersions(d.version, d.self.latest)
	return known && comparison < 0
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

type packageState int

const (
	stateChecking packageState = iota
	stateCurrent
	stateUpdate
	stateMissing
	stateIncomplete
	stateCheckFailed
	stateUnknown
	stateAhead
)

// packageRow is what one list row shows. A version that could not be read says
// why, and only a successful comparison can ever report an update.
type packageRow struct {
	installed, latest, channel string
	state                      packageState
}

func (d *dashboardModel) row(p PackageStatus) packageRow {
	v := d.versions[p.Name]
	row := packageRow{installed: v.installed, latest: v.latest, channel: v.channel, state: stateUnknown}
	if row.channel == "" {
		row.channel = p.Channel
	}
	switch {
	case !p.Installed && !p.Partial:
		row.installed = ""
	case v.installedChecking:
		row.installed = "…"
	case v.installedErr != nil || row.installed == "":
		if p.Installed {
			row.installed = versionFailure(v.installedErr)
		}
	}
	if !v.latestChecking && (v.latestErr != nil || row.latest == "") {
		row.latest = versionFailure(v.latestErr)
	}
	switch {
	case !p.Installed && !p.Partial:
		row.state = stateMissing
	case p.Partial:
		row.state = stateIncomplete
	case v.installedChecking || v.latestChecking:
		row.state = stateChecking
	case v.installedErr != nil || v.latestErr != nil:
		row.state = stateCheckFailed
	default:
		if comparison, ok := compareVersions(v.installed, v.latest); ok {
			switch comparison {
			case -1:
				row.state = stateUpdate
			case 0:
				row.state = stateCurrent
			case 1:
				row.state = stateAhead
			}
		}
	}
	return row
}
