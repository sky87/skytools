package skytools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeReleases serves skytools releases the way github.com does: latest
// redirects to a tag, latest/download/<file> redirects to that tag's file.
type fakeReleases struct {
	*httptest.Server
	mu       sync.Mutex
	latest   string
	files    map[string]string // "<tag>/<file>" -> contents
	requests map[string]int
	block    chan struct{} // when set, binary downloads wait on it or the client
}

func newFakeReleases(t *testing.T, latest string) *fakeReleases {
	t.Helper()
	f := &fakeReleases{latest: latest, files: map[string]string{}, requests: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeReleases) URL() string { return f.Server.URL + "/releases" }

func (f *fakeReleases) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests[r.URL.Path]++
	latest, block := f.latest, f.block
	rest, _ := strings.CutPrefix(r.URL.Path, "/releases/")
	f.mu.Unlock()
	switch {
	case rest == "latest":
		http.Redirect(w, r, "/releases/tag/"+latest, http.StatusFound)
	case strings.HasPrefix(rest, "tag/"):
		fmt.Fprintln(w, "<html>release page</html>")
	case strings.HasPrefix(rest, "latest/download/"):
		http.Redirect(w, r, "/releases/download/"+latest+"/"+strings.TrimPrefix(rest, "latest/download/"), http.StatusFound)
	case strings.HasPrefix(rest, "download/"):
		key := strings.TrimPrefix(rest, "download/")
		if block != nil && strings.Contains(key, "/skytools_") {
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		f.mu.Lock()
		body, ok := f.files[key]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	default:
		http.NotFound(w, r)
	}
}

func scriptAsset(t *testing.T) string {
	t.Helper()
	asset, err := selfUpdateAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	return asset
}

// publish adds a release whose binary is a script reporting `reports`, with
// SHA256SUMS listing it and the repository's install.sh.
func (f *fakeReleases) publish(t *testing.T, tag, reports string) {
	t.Helper()
	asset := scriptAsset(t)
	script, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	binary := "#!/bin/sh\necho 'skytools " + reports + "'\n"
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[tag+"/"+asset] = binary
	f.files[tag+"/install.sh"] = string(script)
	f.files[tag+"/SHA256SUMS"] = digestOf([]byte(binary)) + "  " + asset + "\n" + digestOf(script) + "  install.sh\n"
}

func (f *fakeReleases) set(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if value == "" {
		delete(f.files, key)
	} else {
		f.files[key] = value
	}
}

func (f *fakeReleases) count(substring string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for path, n := range f.requests {
		if strings.Contains(path, substring) {
			total += n
		}
	}
	return total
}

// runInstallScript runs the repository's install.sh as a user would through
// curl | sh, with only the given environment on top of a basic PATH.
func runInstallScript(t *testing.T, home, path string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", "../../install.sh")
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + path}, env...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func stagedFiles(t *testing.T, dir string) []string {
	t.Helper()
	staged, _ := filepath.Glob(filepath.Join(dir, ".skytools-update-*"))
	sort.Strings(staged)
	return staged
}

func TestInstallScriptInstallsIntoLocalBinByDefault(t *testing.T) {
	releases := newFakeReleases(t, "v9.8.7")
	releases.publish(t, "v9.8.7", "v9.8.7")
	home := t.TempDir()
	output, err := runInstallScript(t, home, "/usr/bin:/bin", "SKYTOOLS_RELEASES="+releases.URL())
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	binary := filepath.Join(home, ".local/bin/skytools")
	info, statErr := os.Stat(binary)
	if statErr != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("installed %v %v: %s", info, statErr, output)
	}
	for _, want := range []string{"installed skytools v9.8.7 at " + binary, "is not on PATH"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %s", want, output)
		}
	}
	if releases.count("/api") != 0 {
		t.Fatal("used an API")
	}
}

func TestInstallScriptUpdatesTheCopyOnPathThroughItsSymlink(t *testing.T) {
	releases := newFakeReleases(t, "v9.8.7")
	releases.publish(t, "v9.8.7", "v9.8.7")
	home, tools := t.TempDir(), t.TempDir()
	real := filepath.Join(tools, "real-skytools")
	if err := atomicWrite(real, []byte("#!/bin/sh\necho 'skytools v1.0.0'\n"), 0750); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tools, "bin")
	if err := link(real, filepath.Join(bin, "skytools")); err != nil {
		t.Fatal(err)
	}
	output, err := runInstallScript(t, home, bin+":/usr/bin:/bin", "SKYTOOLS_RELEASES="+releases.URL())
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	data, _ := os.ReadFile(real)
	info, _ := os.Stat(real)
	if target, err := os.Readlink(filepath.Join(bin, "skytools")); err != nil || target != real || !strings.Contains(string(data), "v9.8.7") || info.Mode().Perm() != 0750 {
		t.Fatalf("link=%q data=%q mode=%v: %s", target, data, info.Mode(), output)
	}
	if exists(filepath.Join(home, ".local/bin/skytools")) || len(stagedFiles(t, tools)) != 0 {
		t.Fatalf("installed a second copy or left staging: %s", output)
	}
}

func TestInstallScriptKeepsCurrentOrNewerUnlessAVersionIsAsked(t *testing.T) {
	for _, tc := range []struct{ installed, asked, want string }{
		{"v9.8.7", "", "v9.8.7"}, {"v10.0.0", "", "v10.0.0"}, {"v10.0.0", "v9.8.7", "v9.8.7"},
	} {
		t.Run(tc.installed+"-"+tc.asked, func(t *testing.T) {
			releases := newFakeReleases(t, "v9.8.7")
			releases.publish(t, "v9.8.7", "v9.8.7")
			home := t.TempDir()
			binary := filepath.Join(home, ".local/bin/skytools")
			if err := atomicWrite(binary, []byte("#!/bin/sh\necho 'skytools "+tc.installed+"'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			output, err := runInstallScript(t, home, "/usr/bin:/bin", "SKYTOOLS_RELEASES="+releases.URL(), "SKYTOOLS_VERSION="+tc.asked)
			if err != nil {
				t.Fatalf("%v: %s", err, output)
			}
			if data, _ := os.ReadFile(binary); !strings.Contains(string(data), tc.want) {
				t.Fatalf("binary %q: %s", data, output)
			}
			if downloads := releases.count("/skytools_"); (tc.want == tc.installed) != (downloads == 0) {
				t.Fatalf("binary downloads = %d: %s", downloads, output)
			}
		})
	}
}

func TestInstallScriptFailureKeepsTheWorkingBinary(t *testing.T) {
	asset := scriptAsset(t)
	for _, failure := range []string{"checksum", "missing checksum", "download", "wrong version", "does not run", "prerelease tag", "no release"} {
		t.Run(failure, func(t *testing.T) {
			releases := newFakeReleases(t, "v9.8.7")
			releases.publish(t, "v9.8.7", "v9.8.7")
			switch failure {
			case "checksum":
				releases.set("v9.8.7/SHA256SUMS", strings.Repeat("0", 64)+"  "+asset+"\n")
			case "missing checksum":
				releases.set("v9.8.7/SHA256SUMS", strings.Repeat("0", 64)+"  skytools_other_platform\n")
			case "download":
				releases.set("v9.8.7/"+asset, "")
			case "wrong version":
				releases.publish(t, "v9.8.7", "v9.8.6")
			case "does not run":
				binary := "not an executable\n"
				releases.set("v9.8.7/"+asset, binary)
				releases.set("v9.8.7/SHA256SUMS", digestOf([]byte(binary))+"  "+asset+"\n")
			case "prerelease tag":
				releases.latest = "v9.8.8-rc1"
			case "no release":
				releases.latest = "../../main"
			}
			home := t.TempDir()
			binary := filepath.Join(home, ".local/bin/skytools")
			old := "#!/bin/sh\necho 'skytools v1.0.0'\n"
			if err := atomicWrite(binary, []byte(old), 0755); err != nil {
				t.Fatal(err)
			}
			output, err := runInstallScript(t, home, "/usr/bin:/bin", "SKYTOOLS_RELEASES="+releases.URL())
			if err == nil {
				t.Fatalf("accepted a broken release: %s", output)
			}
			if data, _ := os.ReadFile(binary); string(data) != old {
				t.Fatalf("replaced the working binary: %s", output)
			}
			if staged := stagedFiles(t, filepath.Dir(binary)); len(staged) != 0 {
				t.Fatalf("left %v: %s", staged, output)
			}
		})
	}
}

func TestInstallScriptIsValidShell(t *testing.T) {
	// sh -n catches syntax errors on the shell the tests run with.
	if output, err := exec.Command("sh", "-n", "../../install.sh").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
}
