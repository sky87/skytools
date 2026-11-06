package skytools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T) *manager {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m, err := newManager(context.Background(), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// The test container runs as root; never let a test pick the path that
	// runs apt-get directly.
	m.euid = func() int { return 1000 }
	return m
}

func TestUnknownCommandNamesItself(t *testing.T) {
	var output, errOutput strings.Builder
	if code := Run(context.Background(), []string{"instal"}, &output, &errOutput, "test"); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if got := errOutput.String(); got != "skytools: unknown command \"instal\"; run skytools --help\n" {
		t.Fatalf("stderr = %q", got)
	}
	output.Reset()
	if code := Run(context.Background(), []string{"--help"}, &output, &errOutput, "test"); code != 0 {
		t.Fatalf("help exit %d", code)
	}
	for _, command := range []string{"ensure", "update", "self-update", "status --versions", "list", "version", "deps install", "setup", "env"} {
		if !strings.Contains(output.String(), command) {
			t.Errorf("help omits %q", command)
		}
	}
}

func TestCLIValidationAndReadOnlyCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 0}, {[]string{"--help"}, 0}, {[]string{"version"}, 0},
		{[]string{"unknown"}, 2}, {[]string{"ensure", "--only", "no-such-tool"}, 2},
		{[]string{"ensure|update"}, 2},
		{[]string{"status", "extra"}, 2}, {[]string{"env", "extra"}, 2},
		{[]string{"self-update", "extra"}, 2}, {[]string{"self-update", "--only", "go"}, 2}, {[]string{"self-update", "--help"}, 0},
		{[]string{"list"}, 0}, {[]string{"env"}, 0}, {[]string{"status", "--only", "go,gh"}, 0},
	} {
		if code := Run(context.Background(), tc.args, io.Discard, io.Discard, "test"); code != tc.code {
			t.Errorf("Run(%v) = %d, want %d", tc.args, code, tc.code)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read-only commands changed HOME: %v", entries)
	}
}

func TestSelectionPreservesOrderAndAddsNodeForPi(t *testing.T) {
	m := testManager(t)
	tools, err := selectTools(m.tools(), "pi,uv,pi", true)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.name)
	}
	if got := strings.Join(names, ","); got != "uv,node,pi" {
		t.Fatalf("selected %s", got)
	}
	tools, err = selectTools(m.tools(), "pi", false)
	if err != nil || len(tools) != 1 || tools[0].name != "pi" {
		t.Fatalf("status selection: %v, %v", tools, err)
	}
	if _, err := selectTools(m.tools(), "uv,", true); err == nil {
		t.Fatal("accepted empty tool name")
	}
}

func TestShellSetupPreservesFilesAndIsIdempotent(t *testing.T) {
	m := testManager(t)
	original := []byte("export USER_SETTING=preserved")
	if err := atomicWrite(m.at(".go/current/bin/go"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	name := m.at(".bash_profile")
	if err := os.WriteFile(name, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.at("dotfiles"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.at("dotfiles/zshrc"), []byte("# custom zsh\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(m.at("dotfiles/zshrc"), m.at(".zshrc")); err != nil {
		t.Fatal(err)
	}
	if err := m.setupShell(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(name)
	if err := m.setupShell(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(name)
	if !bytes.Equal(first, second) || strings.Count(string(second), shellMarker) != 1 || !bytes.HasPrefix(second, original) {
		t.Fatalf("profile was damaged or duplicated: %q", second)
	}
	backup, _ := os.ReadFile(name + ".skytools.bak")
	if !bytes.Equal(backup, original) {
		t.Fatalf("backup = %q", backup)
	}
	info, _ := os.Stat(name)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("profile mode changed to %v", info.Mode())
	}
	if _, err := os.Readlink(m.at(".zshrc")); err != nil {
		t.Fatalf("dotfile symlink was replaced: %v", err)
	}
	output, err := m.output("bash", "-c", `. "$HOME/.bash_profile"; first=$PATH; . "$HOME/.bash_profile"; test "$first" = "$PATH"; printf '%s' "$USER_SETTING:$GOROOT"`)
	if err != nil || output != "preserved:"+m.at(".go/current") {
		t.Fatalf("shell environment: %q, %v", output, err)
	}
}

func TestShellSetupRefusesSymlinkOutsideHome(t *testing.T) {
	m := testManager(t)
	external := filepath.Join(t.TempDir(), "profile")
	if err := os.WriteFile(external, []byte("unchanged"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, m.at(".profile")); err != nil {
		t.Fatal(err)
	}
	if err := m.setupShell(); err == nil {
		t.Fatal("accepted external dotfile")
	}
	data, _ := os.ReadFile(external)
	if string(data) != "unchanged" {
		t.Fatal("external dotfile changed")
	}
}

func TestExistingToolEnsureDoesNotDownloadOrUpdate(t *testing.T) {
	m := testManager(t)
	name := m.at(".local/bin/uv")
	if err := atomicWrite(name, []byte("#!/bin/sh\nprintf updated > \"$HOME/update-marker\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	m.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("ensure contacted network for installed uv")
		return nil, nil
	})
	selected, _ := selectTools(m.tools(), "uv", true)
	if err := selected[0].ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.at("update-marker")); !os.IsNotExist(err) {
		t.Fatal("ensure ran update")
	}
	if err := selected[0].update(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.at("update-marker")); err != nil {
		t.Fatal("explicit update did not run")
	}
}

func TestLeanStatusDoesNotStartDownloadingProxy(t *testing.T) {
	m := testManager(t)
	if err := atomicWrite(m.at(".elan/bin/elan"), []byte("#!/bin/sh\necho 'leanprover/lean4:v4.0.0 (default)'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(m.at(".elan/bin/lean"), []byte("#!/bin/sh\ntouch \"$HOME/proxy-started\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	m.out = &output
	selected, _ := selectTools(m.tools(), "lean", false)
	m.status(selected)
	if _, err := os.Stat(m.at("proxy-started")); !os.IsNotExist(err) {
		t.Fatal("status started Lean proxy")
	}
	if !strings.Contains(output.String(), "leanprover/lean4:v4.0.0") {
		t.Fatalf("status: %s", output.String())
	}
}

func TestLeanEnsureInstallsToolchainBeforeSettingDefault(t *testing.T) {
	m := testManager(t)
	script := `#!/bin/sh
printf '%s %s\n' "$1" "${2:-}" >> "$HOME/elan-calls"
case "$1 ${2:-}" in
  'toolchain list')
    if [ -f "$HOME/lean-installed" ]; then echo 'leanprover/lean4:v4.0.0'; fi ;;
  'toolchain install') touch "$HOME/lean-installed" ;;
  'default leanprover/lean4:v4.0.0') test -f "$HOME/lean-installed" ;;
  *) exit 1 ;;
esac
`
	if err := atomicWrite(m.at(".elan/bin/elan"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureLean(); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureLean(); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(m.at("elan-calls"))
	if strings.Count(string(calls), "toolchain install") != 1 {
		t.Fatalf("toolchain was not installed exactly once: %s", calls)
	}
	if !strings.Contains(string(calls), "default leanprover/lean4:v4.0.0") {
		t.Fatalf("default not configured: %s", calls)
	}
}

func TestLockSerializesAndHonorsCancellation(t *testing.T) {
	m := testManager(t)
	locked, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { finished <- m.withLock(func() error { close(locked); <-release; return nil }) }()
	<-locked
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	second := *m
	second.ctx = ctx
	err := second.withLock(func() error { t.Error("entered while lock held"); return nil })
	close(release)
	if err != context.DeadlineExceeded {
		t.Fatalf("lock returned %v", err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := m.withLock(func() error { return nil }); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}

func TestStableCompilerSelection(t *testing.T) {
	for _, input := range []string{"4.14.2 5.2.1 5.10.0 5.11.0~beta1", "5.10.0\n5.2.1\n4.14.2"} {
		got, err := latestStableVersion(input)
		if err != nil || got != "5.10.0" {
			t.Fatalf("%s: %q %v", input, got, err)
		}
	}
	if _, err := latestStableVersion("5.3.0~beta1"); err == nil {
		t.Fatal("accepted prerelease compiler")
	}
}

func TestCorrettoAssets(t *testing.T) {
	for _, tc := range []struct{ system, arch, want string }{
		{"linux", "amd64", "amazon-corretto-25-x64-linux-jdk.tar.gz"},
		{"linux", "arm64", "amazon-corretto-25-aarch64-linux-jdk.tar.gz"},
		{"darwin", "arm64", "amazon-corretto-25-aarch64-macos-jdk.tar.gz"},
	} {
		got, err := correttoAsset(tc.system, tc.arch, "25")
		if err != nil || got != tc.want {
			t.Fatalf("asset: %q %v", got, err)
		}
	}
	if _, err := correttoAsset("linux", "amd64", "../25"); err == nil {
		t.Fatal("accepted invalid major")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func tarData(t *testing.T, headers []*tar.Header, bodies []string) []byte {
	t.Helper()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for i, header := range headers {
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(bodies) > i {
			if _, err := tw.Write([]byte(bodies[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestArchiveRejectsTraversalAndLinkEscapes(t *testing.T) {
	for _, headers := range [][]*tar.Header{
		{{Name: "../escape", Mode: 0644, Typeflag: tar.TypeReg}},
		{{Name: "/absolute", Mode: 0644, Typeflag: tar.TypeReg}},
		{{Name: "link", Linkname: "../escape", Typeflag: tar.TypeSymlink}},
		{{Name: "link", Linkname: "/tmp", Typeflag: tar.TypeSymlink}},
		{{Name: "link", Linkname: "safe", Typeflag: tar.TypeSymlink}, {Name: "link/file", Mode: 0644, Typeflag: tar.TypeReg}},
		{{Name: "link", Linkname: "../escape", Typeflag: tar.TypeLink}},
	} {
		if err := extractTar(bytes.NewReader(tarData(t, headers, nil)), t.TempDir()); err == nil {
			t.Fatalf("accepted unsafe archive: %v", headers)
		}
	}
}

func TestGoInstallUsesChecksumAndKeepsExistingReleaseOnFailure(t *testing.T) {
	m := testManager(t)
	contents := "#!/bin/sh\necho go version go1.26.0\n"
	nativeVersion := "go1.26.0\n"
	archive := tarData(t, []*tar.Header{
		{Name: "go/bin/go", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(contents))},
		{Name: "go/VERSION", Mode: 0644, Typeflag: tar.TypeReg, Size: int64(len(nativeVersion))},
	}, []string{contents, nativeVersion})
	hash := sha256.Sum256(archive)
	checksum := hex.EncodeToString(hash[:])
	version := "go1.26.0"
	filename := version + "." + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if req.URL.Query().Get("mode") == "json" {
			fmt.Fprintf(w, `[{"version":%q,"stable":true,"files":[{"filename":%q,"os":%q,"arch":%q,"kind":"archive","sha256":%q}]}]`, version, filename, runtime.GOOS, runtime.GOARCH, checksum)
			return
		}
		w.Write(archive)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		copy := req.Clone(req.Context())
		copy.URL.Scheme, copy.URL.Host = base.Scheme, base.Host
		return http.DefaultTransport.RoundTrip(copy)
	})
	if err := m.installGo(); err != nil {
		t.Fatal(err)
	}
	if !executable(m.at(".go/current/bin/go")) {
		t.Fatal("installed executable missing")
	}
	first, _ := os.Readlink(m.at(".go/current"))
	selected, _ := selectTools(m.tools(), "go", true)
	before := requests
	if err := selected[0].ensure(); err != nil {
		t.Fatal(err)
	}
	if requests != before {
		t.Fatal("ensure downloaded an existing tool")
	}
	version = "go1.26.1"
	filename = version + "." + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	checksum = strings.Repeat("0", 64)
	if err := m.installGo(); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("corrupt archive: %v", err)
	}
	after, _ := os.Readlink(m.at(".go/current"))
	if after != first || !executable(m.at(".go/current/bin/go")) {
		t.Fatal("failed update damaged working release")
	}
	staging, _ := filepath.Glob(m.at(".go/releases/.download-*"))
	if len(staging) != 0 {
		t.Fatalf("staging directories leaked: %v", staging)
	}
}

func TestLatestGoSkipsUnstableOrWrongPlatform(t *testing.T) {
	var releases []goRelease
	data := `[{"version":"go1.27rc1","stable":false,"files":[]},{"version":"go1.26.0","stable":true,"files":[{"filename":"go1.26.0.linux-arm64.tar.gz","os":"linux","arch":"arm64","kind":"archive","sha256":"` + strings.Repeat("a", 64) + `"}]}]`
	if err := json.Unmarshal([]byte(data), &releases); err != nil {
		t.Fatal(err)
	}
	version, _, _, err := latestGo(releases, "linux", "arm64")
	if err != nil || version != "go1.26.0" {
		t.Fatalf("release: %q %v", version, err)
	}
	if _, _, _, err := latestGo(releases, "linux", "amd64"); err == nil {
		t.Fatal("accepted wrong architecture")
	}
}

func TestChecksumsMatchExactAsset(t *testing.T) {
	hash := strings.Repeat("a", 64)
	got, err := checksumFor([]byte(hash+"  gh_correct.tar.gz\n"), "gh_correct.tar.gz")
	if err != nil || got != hash {
		t.Fatalf("checksum: %q %v", got, err)
	}
	if _, err := checksumFor([]byte(hash+"  gh_other.tar.gz\n"), "gh_correct.tar.gz"); err == nil {
		t.Fatal("accepted wrong asset checksum")
	}
}

func TestDownloadRetriesTransientServerFailure(t *testing.T) {
	m := testManager(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		if requests == 1 {
			http.Error(w, "temporary failure", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, "verified payload")
	}))
	defer server.Close()
	var data bytes.Buffer
	if err := m.download(server.URL, &data); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || data.String() != "verified payload" {
		t.Fatalf("requests=%d, payload=%q", requests, data.String())
	}
}
