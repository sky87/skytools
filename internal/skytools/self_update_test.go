package skytools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

type selfUpdateFixture struct {
	m                           *manager
	path, body, checksum, asset string
	metadata                    string // where /releases/latest redirects
	listed                      string // the asset SHA256SUMS lists
	binaryStatus                int
	beforeBinary                func(*http.Request) error
	requests                    map[string]int
}

func newSelfUpdateFixture(t *testing.T) *selfUpdateFixture {
	t.Helper()
	m := testManager(t)
	asset, err := selfUpdateAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	f := &selfUpdateFixture{m: m, path: m.at("bin/skytools"), asset: asset, body: "#!/bin/sh\necho skytools v9.8.7\n", binaryStatus: 200, requests: map[string]int{}}
	if err := atomicWrite(f.path, []byte("working old executable\n"), 0750); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(f.body))
	f.checksum = hex.EncodeToString(hash[:])
	base := "https://github.com/sky87/skytools/releases/download/v9.8.7/"
	f.metadata = "https://github.com/sky87/skytools/releases/tag/v9.8.7"
	f.listed = asset
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		url := req.URL.String()
		f.requests[url]++
		body, status := "", 200
		switch url {
		case "https://github.com/sky87/skytools/releases/latest":
			return redirectResponse(f.metadata), nil
		case base + "SHA256SUMS":
			body = f.checksum + "  " + f.listed + "\n"
		case base + asset:
			if f.beforeBinary != nil {
				if err := f.beforeBinary(req); err != nil {
					return nil, err
				}
			}
			body, status = f.body, f.binaryStatus
		default:
			return nil, fmt.Errorf("unexpected self-update URL %s", url)
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return f
}

func (f *selfUpdateFixture) setBody(body string) {
	f.body = body
	hash := sha256.Sum256([]byte(body))
	f.checksum = hex.EncodeToString(hash[:])
}

func (f *selfUpdateFixture) assertNoStaging(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(f.path), ".skytools-update-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("staging files=%v err=%v", files, err)
	}
	if exists(f.m.at(".profile")) || exists(f.m.at(".local/state/skytools")) {
		t.Fatal("self-update changed shell files or created persistent state")
	}
}

func TestSelfUpdateUsesLatestVerifiedReleaseAndPreservesAliasAndPermissions(t *testing.T) {
	f := newSelfUpdateFixture(t)
	before, _ := os.Stat(f.path)
	alias := filepath.Join(filepath.Dir(f.path), "sky-tools")
	if err := os.Symlink("skytools", alias); err != nil {
		t.Fatal(err)
	}
	if err := f.m.selfUpdate("v1.0.0", alias); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.path)
	after, statErr := os.Stat(f.path)
	link, linkErr := os.Readlink(alias)
	if err != nil || statErr != nil || linkErr != nil || link != "skytools" || string(data) != f.body || after.Mode().Perm() != 0750 || os.SameFile(before, after) {
		t.Fatalf("replacement data=%q link=%q errors=%v,%v,%v", data, link, err, statErr, linkErr)
	}
	if len(f.requests) != 3 {
		t.Fatalf("unexpected requests: %v", f.requests)
	}
	f.assertNoStaging(t)
}

func TestSelfUpdateKeepsCurrentOrNewerWithoutDownloadingAssets(t *testing.T) {
	for _, current := range []string{"v9.8.7", "v10.0.0"} {
		t.Run(current, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			if err := f.m.selfUpdate(current, f.path); err != nil {
				t.Fatal(err)
			}
			if len(f.requests) != 1 {
				t.Fatalf("downloaded an unnecessary asset: %v", f.requests)
			}
			data, _ := os.ReadFile(f.path)
			if string(data) != "working old executable\n" {
				t.Fatal("changed an up-to-date executable")
			}
			f.assertNoStaging(t)
		})
	}
}

func TestSelfUpdateFailureKeepsWorkingExecutable(t *testing.T) {
	for _, failure := range []string{"checksum", "missing checksum", "download", "bad executable", "wrong version", "prerelease", "foreign URL", "missing platform"} {
		t.Run(failure, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			before, _ := os.Stat(f.path)
			switch failure {
			case "checksum":
				f.checksum = strings.Repeat("0", 64)
			case "missing checksum":
				f.checksum = ""
			case "download":
				f.binaryStatus = 404
			case "bad executable":
				f.setBody("not an executable\n")
			case "wrong version":
				f.setBody("#!/bin/sh\necho skytools v9.8.6\n")
			case "prerelease":
				f.metadata = strings.Replace(f.metadata, "v9.8.7", "v9.8.8-rc1", 1)
			case "foreign URL":
				f.metadata = strings.ReplaceAll(f.metadata, "https://github.com/", "https://example.invalid/")
			case "missing platform":
				f.listed = "skytools_other_platform"
			}
			if err := f.m.selfUpdate("dev", f.path); err == nil {
				t.Fatal("accepted broken release")
			}
			data, _ := os.ReadFile(f.path)
			after, _ := os.Stat(f.path)
			if string(data) != "working old executable\n" || !os.SameFile(before, after) {
				t.Fatal("failed update changed the working executable")
			}
			f.assertNoStaging(t)
		})
	}
}

func TestSelfUpdateCancellationPreservesExecutable(t *testing.T) {
	for _, phase := range []string{"download", "validation"} {
		t.Run(phase, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			if phase == "download" {
				f.beforeBinary = func(req *http.Request) error { <-req.Context().Done(); return req.Context().Err() }
			} else {
				f.setBody("#!/bin/sh\nsleep 30\n")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			f.m.ctx = ctx
			start := time.Now()
			err := f.m.selfUpdate("dev", f.path)
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
				t.Fatalf("cancellation: %v after %s", err, time.Since(start))
			}
			data, _ := os.ReadFile(f.path)
			if string(data) != "working old executable\n" {
				t.Fatal("cancelled update changed the working executable")
			}
			f.assertNoStaging(t)
		})
	}
}

func TestSelfUpdateDoesNotClobberAnotherReplacement(t *testing.T) {
	f := newSelfUpdateFixture(t)
	f.beforeBinary = func(*http.Request) error {
		return atomicWrite(f.path, []byte("replacement from another process\n"), 0750)
	}
	if err := f.m.selfUpdate("dev", f.path); err == nil || !strings.Contains(err.Error(), "changed during update") {
		t.Fatalf("ignored concurrent replacement: %v", err)
	}
	data, _ := os.ReadFile(f.path)
	if string(data) != "replacement from another process\n" {
		t.Fatal("overwrote concurrent replacement")
	}
	f.assertNoStaging(t)
}

func TestSelfUpdateRejectsConcurrentUpdater(t *testing.T) {
	f := newSelfUpdateFixture(t)
	file, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	if err := f.m.selfUpdate("dev", f.path); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("ignored updater lock: %v", err)
	}
	if len(f.requests) != 0 {
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
