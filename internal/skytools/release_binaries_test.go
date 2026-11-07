package skytools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBtopUpdateChecksDigestBeforeReplacingBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux binary installer")
	}
	m := testManager(t)
	body := "#!/bin/sh\necho btop version: 1.2.3\n"
	archive := tarData(t, []*tar.Header{{Name: "btop/bin/btop", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(body))}}, []string{body})
	hash := sha256.Sum256(archive)
	digest := "sha256:" + hex.EncodeToString(hash[:])
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	asset := "btop-" + arch + "-unknown-linux-musl.tar.gz"
	downloads := 0
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		var data []byte
		switch req.URL.String() {
		case "https://api.github.com/repos/aristocratos/btop/releases/latest":
			data = []byte(fmt.Sprintf(`{"tag_name":"v1.2.3","assets":[{"name":%q,"digest":%q,"browser_download_url":%q}]}`, asset, digest, "https://github.com/aristocratos/btop/releases/download/v1.2.3/"+asset))
		case "https://github.com/aristocratos/btop/releases/download/v1.2.3/" + asset:
			downloads++
			data = archive
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	if err := m.installReleaseBinary("btop"); err != nil {
		t.Fatal(err)
	}
	binary := m.at(".local/bin/btop")
	if data, err := os.ReadFile(binary); err != nil || string(data) != body || !executable(binary) {
		t.Fatalf("binary=%q err=%v", data, err)
	}
	for _, invalid := range []string{"", "sha256:" + strings.Repeat("0", 64)} {
		digest = invalid
		before := downloads
		if err := m.installReleaseBinary("btop"); err == nil {
			t.Fatal("accepted missing or incorrect checksum")
		}
		if invalid == "" && downloads != before {
			t.Fatal("downloaded without a publisher digest")
		}
		data, err := os.ReadFile(binary)
		if err != nil || string(data) != body {
			t.Fatal("failed update replaced working binary")
		}
	}
	staging, _ := filepath.Glob(m.at(".local/state/skytools/.download-*"))
	if len(staging) != 0 {
		t.Fatalf("staging directories leaked: %v", staging)
	}
}

// releaseFixture serves one GitHub release whose assets carry real digests.
func releaseFixture(t *testing.T, m *manager, repository, tag string, assets map[string][]byte) *int {
	t.Helper()
	downloads := 0
	var list []string
	for name, data := range assets {
		hash := sha256.Sum256(data)
		url := "https://github.com/" + repository + "/releases/download/" + tag + "/" + name
		list = append(list, fmt.Sprintf(`{"name":%q,"digest":"sha256:%s","browser_download_url":%q}`, name, hex.EncodeToString(hash[:]), url))
	}
	metadata := fmt.Sprintf(`{"tag_name":%q,"assets":[%s]}`, tag, strings.Join(list, ","))
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		var data []byte
		if req.URL.String() == "https://api.github.com/repos/"+repository+"/releases/latest" {
			data = []byte(metadata)
		} else if name, ok := strings.CutPrefix(req.URL.String(), "https://github.com/"+repository+"/releases/download/"+tag+"/"); ok && assets[name] != nil {
			downloads++
			data = assets[name]
		} else {
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	return &downloads
}

func TestReleaseBinariesInstallRawZipAndTarAssets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux binary installer")
	}
	m := testManager(t)
	jq := "#!/bin/sh\necho jq-1.8.2\n"
	releaseFixture(t, m, "jqlang/jq", "jq-1.8.2", map[string][]byte{"jq-linux-" + runtime.GOARCH: []byte(jq)})
	if err := m.installReleaseBinary("jq"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(m.at(".local/bin/jq")); err != nil || string(data) != jq || !executable(m.at(".local/bin/jq")) {
		t.Fatalf("jq=%q err=%v", data, err)
	}
	if version, err := m.installedVersion(m.ctx, PackageStatus{Name: "jq", Installed: true, Path: m.at(".local/bin/jq")}); err != nil || version != "1.8.2" {
		t.Fatalf("jq version = %q, %v", version, err)
	}

	// The zip records no executable bit, and its own sg is not installed: sg
	// links to ast-grep.
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"ast-grep", "sg"} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		file.Write([]byte("#!/bin/sh\necho ast-grep 0.51.1\n"))
	}
	writer.Close()
	// Every Debian system has /usr/bin/sg (newgrp), and the fixture adds one where
	// it is missing. It is not a partial ast-grep.
	system := t.TempDir()
	if err := atomicWrite(filepath.Join(system, "sg"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for i, entry := range m.env {
		if strings.HasPrefix(entry, "PATH=") {
			m.env[i] = entry + ":" + system
		}
	}
	if _, err := m.lookup("sg"); err != nil {
		t.Fatalf("no sg on PATH: %v", err)
	}
	if s := m.scan([]tool{{name: "ast-grep"}})[0]; s.Installed || s.Partial {
		t.Fatalf("system sg reads as ast-grep: %+v", s)
	}
	arch := rustArch[runtime.GOARCH]
	releaseFixture(t, m, "ast-grep/ast-grep", "0.51.1", map[string][]byte{"app-" + arch + "-unknown-linux-gnu.zip": archive.Bytes()})
	if err := m.installReleaseBinary("ast-grep"); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(m.at(".local/bin/sg")); !executable(m.at(".local/bin/ast-grep")) || err != nil || target != "ast-grep" {
		t.Fatalf("ast-grep not executable or sg -> %q, %v", target, err)
	}
	os.Remove(m.at(".local/bin/sg"))
	if s := m.managedStatus("ast-grep"); s.Installed || !s.Partial {
		t.Fatalf("ast-grep without sg reads as %+v", s)
	}
	var ensure func() error
	for _, t := range m.tools() {
		if t.name == "ast-grep" {
			ensure = t.ensure
		}
	}
	if err := ensure(); err != nil || !executable(m.at(".local/bin/sg")) {
		t.Fatalf("ensure did not restore sg: %v", err)
	}

	body := "#!/bin/sh\necho ripgrep 15.2.0\n"
	dir := "ripgrep-15.2.0-" + arch + "-unknown-linux-musl"
	good := tarData(t, []*tar.Header{{Name: dir + "/rg", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(body))}}, []string{body})
	asset := dir + ".tar.gz"
	releaseFixture(t, m, "BurntSushi/ripgrep", "15.2.0", map[string][]byte{asset: good})
	if err := m.installReleaseBinary("ripgrep"); err != nil {
		t.Fatal(err)
	}
	moved := tarData(t, []*tar.Header{{Name: "rg", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(body))}}, []string{body})
	releaseFixture(t, m, "BurntSushi/ripgrep", "15.2.0", map[string][]byte{asset: moved})
	if err := m.installReleaseBinary("ripgrep"); err == nil || !strings.Contains(err.Error(), "unexpected ripgrep archive layout") {
		t.Fatalf("changed layout accepted: %v", err)
	}
	if data, _ := os.ReadFile(m.at(".local/bin/rg")); string(data) != body {
		t.Fatal("failed update replaced rg")
	}
	if s := m.managedStatus("ripgrep"); !s.Installed || s.Path != m.at(".local/bin/rg") {
		t.Fatalf("ripgrep status %+v", s)
	}
}

func TestNeovimInstallsReleaseTreeAndReadsItsVersionOffline(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux binary installer")
	}
	m := testManager(t)
	arch := linuxArch[runtime.GOARCH]
	root := "nvim-linux-" + arch
	nvim := "#!/bin/sh\necho NVIM v0.12.6\n"
	archive := tarData(t, []*tar.Header{
		{Name: root + "/bin/nvim", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(nvim))},
		{Name: root + "/share/nvim/runtime/filetype.lua", Mode: 0644, Typeflag: tar.TypeReg, Size: 2},
	}, []string{nvim, "--"})
	downloads := releaseFixture(t, m, "neovim/neovim", "v0.12.6", map[string][]byte{root + ".tar.gz": archive})
	if s := m.managedStatus("neovim"); s.Installed || s.Partial {
		t.Fatalf("empty home reads as %+v", s)
	}
	if err := m.installNeovim(); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(m.at(".neovim/current")); err != nil || target != m.at(".neovim/releases/0.12.6") {
		t.Fatalf("current -> %q, %v", target, err)
	}
	s := m.managedStatus("neovim")
	if !s.Installed || s.Version != "0.12.6" || s.Path != m.at(".neovim/current/bin/nvim") {
		t.Fatalf("status %+v", s)
	}
	if err := m.installNeovim(); err != nil || *downloads != 1 {
		t.Fatalf("reinstalled the same release: downloads=%d err=%v", *downloads, err)
	}
	staging, _ := filepath.Glob(m.at(".neovim/releases/.download-*"))
	if len(staging) != 0 {
		t.Fatalf("staging directories leaked: %v", staging)
	}
	os.RemoveAll(m.at(".neovim/releases/0.12.6/share"))
	if s := m.managedStatus("neovim"); s.Installed || !s.Partial {
		t.Fatalf("release without runtime files reads as %+v", s)
	}
}

func TestLazygitVersionSkipsItsBuildFields(t *testing.T) {
	m := testManager(t)
	script := "#!/bin/sh\necho 'commit=0a1b2c3, build date=2026-10-01T10:00:00Z, build source=binaryRelease, version=0.66.2, os=linux, arch=arm64, git version=2.47.3'\n"
	if err := atomicWrite(m.at(".local/bin/lazygit"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := m.installedVersion(m.ctx, PackageStatus{Name: "lazygit", Installed: true, Path: m.at(".local/bin/lazygit")}); err != nil || got != "0.66.2" {
		t.Fatalf("version = %q, %v", got, err)
	}
}

// Interactive bash and zsh hook direnv and zoxide only when skytools installed them.
func TestShellEnvironmentHooksManagedDirenvAndZoxide(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	home := t.TempDir()
	run := func() string {
		cmd := exec.Command(bash, "--norc", "--noprofile", "-i", "-c", `. "$HOME/env.sh"; echo "path=$PATH"`)
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "TERM=dumb"}
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: %s", err, output)
		}
		return string(output)
	}
	if err := atomicWrite(filepath.Join(home, "env.sh"), []byte(shellEnv), 0644); err != nil {
		t.Fatal(err)
	}
	if output := run(); strings.Contains(output, "hooked") {
		t.Fatalf("hooked tools that are not installed: %s", output)
	}
	for name, script := range map[string]string{
		"direnv": "#!/bin/sh\n[ \"$1 $2\" = 'hook bash' ] && echo 'echo direnv hooked'\n",
		"zoxide": "#!/bin/sh\n[ \"$1 $2\" = 'init bash' ] && echo 'echo zoxide hooked'\n",
	} {
		if err := atomicWrite(filepath.Join(home, ".local/bin", name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	output := run()
	for _, want := range []string{"direnv hooked", "zoxide hooked", home + "/.neovim/current/bin"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %s", want, output)
		}
	}
}
