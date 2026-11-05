package skytools

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTailscaleInstallVerifiesChecksumBeforeReplacingBinaries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux binary installer")
	}
	m := testManager(t)
	name := "tailscale_1.2.3_" + runtime.GOARCH
	const client, daemon = "#!/bin/sh\necho client\n", "#!/bin/sh\necho daemon\n"
	archive := tarData(t, []*tar.Header{
		{Name: name + "/tailscale", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(client))},
		{Name: name + "/tailscaled", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(daemon))},
	}, []string{client, daemon})
	hash := sha256.Sum256(archive)
	checksum := hex.EncodeToString(hash[:])
	index := fmt.Sprintf(`{"TarballsVersion":"1.2.3","Tarballs":{%q:%q}}`, runtime.GOARCH, name+".tgz")
	downloads := 0
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		var data []byte
		switch req.URL.String() {
		case tailscalePackages + "?mode=json":
			data = []byte(index)
		case tailscalePackages + name + ".tgz.sha256":
			data = []byte(checksum + "\n")
		case tailscalePackages + name + ".tgz":
			downloads++
			data = archive
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	if err := m.installTailscale(); err != nil {
		t.Fatal(err)
	}
	for binary, body := range map[string]string{"tailscale": client, "tailscaled": daemon} {
		path := m.at(".local/bin", binary)
		if data, err := os.ReadFile(path); err != nil || string(data) != body || !executable(path) {
			t.Fatalf("%s: %q, %v", binary, data, err)
		}
	}
	if s := m.managedStatus("tailscale"); !s.Installed || s.Partial {
		t.Fatalf("complete install reads as %+v", s)
	}

	// Every failure must leave the working binaries in place.
	good, goodIndex := checksum, index
	for label, change := range map[string]func(){
		"wrong checksum":   func() { checksum = strings.Repeat("0", 64) },
		"no checksum":      func() { checksum = "not published" },
		"no package":       func() { index = `{"TarballsVersion":"1.2.3","Tarballs":{}}` },
		"path in the name": func() { index = fmt.Sprintf(`{"Tarballs":{%q:"../../evil.tgz"}}`, runtime.GOARCH) },
	} {
		checksum, index = good, goodIndex
		change()
		before := downloads
		if err := m.installTailscale(); err == nil {
			t.Fatalf("%s: accepted", label)
		}
		if label != "wrong checksum" && downloads != before {
			t.Fatalf("%s: downloaded the archive anyway", label)
		}
		if data, err := os.ReadFile(m.at(".local/bin/tailscale")); err != nil || string(data) != client {
			t.Fatalf("%s: failed update replaced the binary", label)
		}
	}
	checksum, index = good, goodIndex

	// An archive missing the daemon must not update the client on its own.
	partial := tarData(t, []*tar.Header{{Name: name + "/tailscale", Mode: 0755, Typeflag: tar.TypeReg, Size: 3}}, []string{"new"})
	hash = sha256.Sum256(partial)
	checksum, archive = hex.EncodeToString(hash[:]), partial
	if err := m.installTailscale(); err == nil {
		t.Fatal("accepted an archive without the daemon")
	}
	if data, _ := os.ReadFile(m.at(".local/bin/tailscale")); string(data) != client {
		t.Fatal("client updated without its daemon")
	}
	if staging, _ := filepath.Glob(m.at(".local/state/skytools/.download-*")); len(staging) != 0 {
		t.Fatalf("staging directories leaked: %v", staging)
	}
}

func TestTailscaleDetection(t *testing.T) {
	m := testManager(t)
	system := t.TempDir()
	m.env = setEnv(m.env, "PATH", system)
	status := func() PackageStatus { return m.scan([]tool{{name: "tailscale", label: "Tailscale"}})[0] }
	if s := status(); s.Installed || s.Partial {
		t.Fatalf("nothing installed reads as %+v", s)
	}
	// apt puts tailscaled in /usr/sbin, which is not on a normal user's PATH, so
	// the client alone counts as a complete system installation.
	if err := os.WriteFile(filepath.Join(system, "tailscale"), []byte("unused"), 0755); err != nil {
		t.Fatal(err)
	}
	if s := status(); !s.Installed || s.Partial || s.Origin != "system" {
		t.Fatalf("system client reads as %+v", s)
	}
	// In the home directory skytools installs both, so one alone is incomplete.
	if err := atomicWrite(m.at(".local/bin/tailscale"), []byte("unused"), 0755); err != nil {
		t.Fatal(err)
	}
	if s := status(); s.Installed || !s.Partial || s.Origin != "managed" {
		t.Fatalf("client without daemon reads as %+v", s)
	}
	if err := atomicWrite(m.at(".local/bin/tailscaled"), []byte("unused"), 0755); err != nil {
		t.Fatal(err)
	}
	if s := status(); !s.Installed || s.Partial || s.Origin != "managed" {
		t.Fatalf("complete managed install reads as %+v", s)
	}
}

func TestTailscaleVersionProbeUsesItsSubcommand(t *testing.T) {
	m := testManager(t)
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf '1.104.5\\n  tailscale commit: abc\\n  go version: go1.26\\n'; else exit 3; fi\n"
	if err := atomicWrite(m.at(".local/bin/tailscale"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := m.installedVersion(m.ctx, PackageStatus{Name: "tailscale", Installed: true, Path: m.at(".local/bin/tailscale")})
	if err != nil || got != "1.104.5" {
		t.Fatalf("version = %q, %v", got, err)
	}
}
