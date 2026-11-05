package skytools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sky87/skytools/internal/termui"
)

func TestDashboardVersionsShowCurrentLatestAndUpdatesAt80Columns(t *testing.T) {
	d := newDashboardModel([]PackageStatus{{Name: "go", Installed: true, Version: "go1.25.0", Channel: "stable"}, {Name: "ocaml", Installed: true, Version: "5.5.2", Channel: "LTS"}, {Name: "uv", Installed: true, Version: "1.0.0", Channel: "stable"}})
	for name, version := range map[string]string{"go": "1.26.2", "ocaml": "4.14.4"} {
		d.applyVersion(dashboardVersionResult{generation: d.versionGeneration, name: name, release: ReleaseInfo{Version: version, Channel: d.versions[name].channel}})
	}
	d.applyVersion(dashboardVersionResult{generation: d.versionGeneration, name: "uv", err: errors.New("offline")})
	if row := d.row(d.packages[0]); row.latest != "1.26.2" || row.state != stateUpdate {
		t.Fatalf("%+v", row)
	}
	if row := d.row(d.packages[1]); row.state != stateAhead {
		t.Fatalf("%+v", row)
	}
	if row := d.row(d.packages[2]); row.state != stateCheckFailed {
		t.Fatalf("%+v", row)
	}
	var ui termui.Context
	d.frame(&ui, 80, 24, termui.Event{})
	frame := ui.EndFrame().Text()
	for _, text := range []string{"go1.25.0", "1.26.2", "↑ update", "4.14.4", "» ahead", "? check failed"} {
		if !strings.Contains(frame, text) {
			t.Errorf("missing %q in frame:\n%s", text, frame)
		}
	}
	old := d.versionGeneration
	d.resetVersions()
	d.applyVersion(dashboardVersionResult{generation: old, name: "go", release: ReleaseInfo{Version: "99.0.0"}})
	if d.versions["go"].latest != "" {
		t.Fatal("stale background result overwrote refreshed state")
	}
}

func TestDashboardVersionWorkerPoolCancelsAndStaysBounded(t *testing.T) {
	var active, peak atomic.Int32
	probe := func(ctx context.Context, name string) (ReleaseInfo, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-ctx.Done()
		return ReleaseInfo{}, ctx.Err()
	}
	var packages []PackageStatus
	for i := 0; i < 16; i++ {
		packages = append(packages, PackageStatus{Name: fmt.Sprint(i)})
	}
	check := startDashboardVersions(context.Background(), 1, packages, dashboardVersionProbes{latest: probe})
	deadline := time.Now().Add(time.Second)
	for peak.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	check.cancel()
	select {
	case <-check.done:
	case <-time.After(time.Second):
		t.Fatal("version checks did not stop")
	}
	if peak.Load() != 4 || active.Load() != 0 {
		t.Fatalf("peak=%d active=%d", peak.Load(), active.Load())
	}
}

func TestInstalledVersionProbeUsesPrimaryExecutableAndCancellation(t *testing.T) {
	m := testManager(t)
	if err := atomicWrite(m.at(".local/bin/btop"), []byte("#!/bin/sh\nprintf '\\033[1mbtop version: 1.4.8\\033[0m\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	p := PackageStatus{Name: "btop", Installed: true, Origin: "managed", Path: m.at(".local/bin/btop")}
	if version, err := m.installedVersion(context.Background(), p); err != nil || version != "1.4.8" {
		t.Fatalf("%q %v", version, err)
	}
	if err := atomicWrite(p.Path, []byte("#!/bin/sh\nsleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := m.installedVersion(ctx, p)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("probe did not cancel promptly: %v", err)
	}
}
