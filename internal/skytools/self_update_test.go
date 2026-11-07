package skytools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newSelfUpdateTarget is an old skytools in a fake home, updated from a fake
// release server that also serves the repository's install.sh.
func newSelfUpdateTarget(t *testing.T) (*manager, *fakeReleases, string) {
	t.Helper()
	m := testManager(t)
	releases := newFakeReleases(t, "v9.8.7")
	releases.publish(t, "v9.8.7", "v9.8.7")
	m.env = setEnv(m.env, "SKYTOOLS_RELEASES", releases.URL())
	path := m.at("bin/skytools")
	if err := atomicWrite(path, []byte("#!/bin/sh\necho 'skytools v1.0.0'\n"), 0750); err != nil {
		t.Fatal(err)
	}
	return m, releases, path
}

func TestSelfUpdateRunsTheReleaseInstallScriptOnTheRunningExecutable(t *testing.T) {
	m, releases, path := newSelfUpdateTarget(t)
	alias := filepath.Join(filepath.Dir(path), "sky-tools")
	if err := os.Symlink("skytools", alias); err != nil {
		t.Fatal(err)
	}
	if err := m.selfUpdate("v1.0.0", alias); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	link, err := os.Readlink(alias)
	if err != nil || link != "skytools" || !strings.Contains(string(data), "v9.8.7") || info.Mode().Perm() != 0750 {
		t.Fatalf("link=%q data=%q mode=%v", link, data, info.Mode())
	}
	// The update went to the running executable, not a new ~/.local/bin copy.
	if exists(m.at(".local/bin/skytools")) || len(stagedFiles(t, filepath.Dir(path))) != 0 {
		t.Fatal("installed a second copy or left staging")
	}
	if releases.count("/download/v9.8.7/install.sh") != 1 || releases.count("/skytools_") != 1 {
		t.Fatalf("requests: %v", releases.requests)
	}
}

func TestSelfUpdateKeepsCurrentOrNewerWithoutDownloadingTheBinary(t *testing.T) {
	for _, installed := range []string{"v9.8.7", "v10.0.0"} {
		t.Run(installed, func(t *testing.T) {
			m, releases, path := newSelfUpdateTarget(t)
			body := "#!/bin/sh\necho 'skytools " + installed + "'\n"
			if err := atomicWrite(path, []byte(body), 0750); err != nil {
				t.Fatal(err)
			}
			if err := m.selfUpdate(installed, path); err != nil {
				t.Fatal(err)
			}
			if data, _ := os.ReadFile(path); string(data) != body || releases.count("/skytools_") != 0 {
				t.Fatalf("changed an up-to-date executable or downloaded it: %v", releases.requests)
			}
		})
	}
}

func TestSelfUpdateNeverRunsAnUnverifiedScript(t *testing.T) {
	for _, failure := range []string{"tampered script", "unlisted script", "no checksums"} {
		t.Run(failure, func(t *testing.T) {
			m, releases, path := newSelfUpdateTarget(t)
			marker := m.at("script-ran")
			switch failure {
			case "tampered script":
				releases.set("v9.8.7/install.sh", "#!/bin/sh\n: > '"+marker+"'\n")
			case "unlisted script":
				releases.set("v9.8.7/SHA256SUMS", strings.Repeat("0", 64)+"  "+scriptAsset(t)+"\n")
			case "no checksums":
				releases.set("v9.8.7/SHA256SUMS", "")
			}
			if err := m.selfUpdate("v1.0.0", path); err == nil {
				t.Fatal("accepted an unverified install.sh")
			}
			if exists(marker) || releases.count("/skytools_") != 0 {
				t.Fatal("ran the script or downloaded the binary")
			}
			if data, _ := os.ReadFile(path); !strings.Contains(string(data), "v1.0.0") {
				t.Fatal("changed the working executable")
			}
		})
	}
}

func TestSelfUpdateReportsAScriptFailureAndKeepsTheExecutable(t *testing.T) {
	m, releases, path := newSelfUpdateTarget(t)
	releases.publish(t, "v9.8.7", "v9.8.6") // the binary reports the wrong version
	err := m.selfUpdate("v1.0.0", path)
	if err == nil || !strings.Contains(err.Error(), "skytools v1.0.0 is unchanged") {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "v1.0.0") || len(stagedFiles(t, filepath.Dir(path))) != 0 {
		t.Fatal("changed the working executable or left staging")
	}
}

func TestSelfUpdateCancellationPreservesExecutable(t *testing.T) {
	m, releases, path := newSelfUpdateTarget(t)
	releases.block = make(chan struct{})
	defer close(releases.block)
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	go func() {
		for releases.count("/skytools_") == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	err := m.selfUpdate("v1.0.0", path)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "v1.0.0") {
		t.Fatal("cancellation changed the executable")
	}
	if staged := stagedFiles(t, filepath.Dir(path)); len(staged) != 0 {
		t.Fatalf("cancellation left %v", staged)
	}
}

func TestSelfUpdateRejectsConcurrentUpdater(t *testing.T) {
	m, releases, path := newSelfUpdateTarget(t)
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if err := m.selfUpdate("dev", path); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("ignored updater lock: %v", err)
	}
	if releases.count("/") != 0 {
		t.Fatal("concurrent updater contacted upstream")
	}
}

func TestSelfUpdatePlatformAssets(t *testing.T) {
	for _, system := range []string{"linux", "darwin"} {
		for _, architecture := range []string{"amd64", "arm64"} {
			asset, err := selfUpdateAsset(system, architecture)
			if err != nil || asset != "skytools_"+system+"_"+architecture {
				t.Fatalf("asset=%q err=%v", asset, err)
			}
		}
	}
	for _, platform := range [][2]string{{"windows", "amd64"}, {"linux", "386"}} {
		if _, err := selfUpdateAsset(platform[0], platform[1]); err == nil {
			t.Fatal("accepted unsupported platform")
		}
	}
}
