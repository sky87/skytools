package skytools

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestLatestJavaLTSFollowsFutureMetadata(t *testing.T) {
	var releases javaReleases
	// Feature 30 is available, but upstream classifies only 29 as the new LTS.
	if err := json.Unmarshal([]byte(`{"available_lts_releases":[8,11,17,21,25,29],"available_releases":[29,30],"most_recent_lts":29}`), &releases); err != nil {
		t.Fatal(err)
	}
	if got, err := latestJavaLTS(releases); got != "29" || err != nil {
		t.Fatalf("latest LTS = %q, %v", got, err)
	}
	for _, releases := range []javaReleases{{}, {AvailableLTS: []int{25}, MostRecentLTS: 29}, {AvailableLTS: []int{-1}, MostRecentLTS: -1}} {
		if _, err := latestJavaLTS(releases); err == nil {
			t.Fatalf("accepted invalid metadata: %+v", releases)
		}
	}
}

func TestCorrettoMajorQueriesMetadataOnlyWhenInstallationNeedsIt(t *testing.T) {
	m := inventoryManager(t)
	calls := 0
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != "https://api.adoptium.net/v3/info/available_releases" {
			t.Fatalf("unexpected metadata source: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"available_lts_releases":[25,29],"most_recent_lts":29}`)), Header: http.Header{}}, nil
	})
	if got, err := m.correttoMajor(); got != "29" || err != nil {
		t.Fatalf("major = %q, %v", got, err)
	}
	if calls != 1 {
		t.Fatalf("got %d metadata requests", calls)
	}
}

func TestGitHubReleaseAcceptsOnlyNumericStableTags(t *testing.T) {
	for _, tc := range []struct{ tag, version string }{
		{"v0.99.1", "0.99.1"}, {"0.99.1", "0.99.1"}, {"jq-1.8.2", "1.8.2"}, {"bun-v1.3.0", "1.3.0"},
		{"v1.0.0-rc1", ""}, {"nightly", ""},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			m := inventoryManager(t)
			m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://github.com/nvm-sh/nvm/releases/latest" {
					t.Fatalf("unexpected endpoint: %s", req.URL)
				}
				return latestTagResponse("nvm-sh/nvm", tc.tag), nil
			})
			got, err := m.githubRelease("nvm-sh/nvm")
			if got.Version != tc.version || (err == nil) != (tc.version != "") {
				t.Fatalf("release = %+v, %v; want %q", got, err, tc.version)
			}
			if err == nil && (got.Tag != tc.tag || got.DownloadURL("a b") != "https://github.com/nvm-sh/nvm/releases/download/"+tc.tag+"/a%20b") {
				t.Fatalf("release = %+v, URL %s", got, got.DownloadURL("a b"))
			}
		})
	}
}

func TestEnsureCompleteNodeAndRustDoNotResolveReleases(t *testing.T) {
	m := inventoryManager(t)
	populateInventory(t, m)
	if err := m.ensureNode(); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureRust(); err != nil {
		t.Fatal(err)
	}
	if exists(m.at("process-started")) {
		t.Fatal("ensure started a complete toolchain")
	}
}

func TestFZFBinaryInstallVerifiesChecksumAndKeepsWorkingBinaryOnFailure(t *testing.T) {
	m := inventoryManager(t)
	contents := "#!/bin/sh\necho fzf\n"
	archive := tarData(t, []*tar.Header{{Name: "fzf", Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(contents))}}, []string{contents})
	hash := sha256.Sum256(archive)
	checksum := hex.EncodeToString(hash[:])
	name := fmt.Sprintf("fzf-0.99.1-%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		var body io.Reader
		switch req.URL.Path {
		case "/junegunn/fzf/releases/download/v0.99.1/fzf_0.99.1_checksums.txt":
			body = strings.NewReader(checksum + "  " + name + "\n")
		case "/junegunn/fzf/releases/download/v0.99.1/" + name:
			body = strings.NewReader(string(archive))
		default:
			t.Fatalf("unexpected release URL %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(body)}, nil
	})
	if err := m.installFZFBinary(githubRelease{Repository: "junegunn/fzf", Tag: "v0.99.1", Version: "0.99.1"}); err != nil {
		t.Fatal(err)
	}
	if !executable(m.at(".fzf/bin/fzf")) {
		t.Fatal("installed fzf missing")
	}
	checksum = strings.Repeat("0", 64)
	if err := m.installFZFBinary(githubRelease{Repository: "junegunn/fzf", Tag: "v0.99.1", Version: "0.99.1"}); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("corrupt archive: %v", err)
	}
	if got := nativeText(m.at(".fzf/bin/fzf")); got != strings.TrimSpace(contents) {
		t.Fatalf("working binary changed: %q", got)
	}
}

func TestRustPartialRepairUsesInstalledStableWithoutNetwork(t *testing.T) {
	m := inventoryManager(t)
	populateInventory(t, m)
	if err := os.Remove(m.at(".rustup/settings.toml")); err != nil {
		t.Fatal(err)
	}
	inventoryFile(t, m, ".cargo/bin/rustup", "#!/bin/sh\nprintf '%s %s' \"$1\" \"$2\" > \"$HOME/rustup-repair\"\n", 0755)
	m.env = []string{"PATH=/usr/bin:/bin", "HOME=" + m.home}
	if err := m.ensureRust(); err != nil {
		t.Fatal(err)
	}
	if got := nativeText(m.at("rustup-repair")); got != "default stable-test" {
		t.Fatalf("repair = %q", got)
	}
}

func TestRustUpdateSelectsStableChannel(t *testing.T) {
	m := inventoryManager(t)
	inventoryFile(t, m, ".cargo/bin/rustup", "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" >> \"$HOME/rustup-calls\"\n", 0755)
	m.env = []string{"PATH=/usr/bin:/bin", "HOME=" + m.home}
	if err := m.updateRust(); err != nil {
		t.Fatal(err)
	}
	if got := nativeText(m.at("rustup-calls")); got != "update stable\ndefault stable" {
		t.Fatalf("update = %q", got)
	}
}

func TestOCamlFollowsTheOfficialLatestFlag(t *testing.T) {
	m := inventoryManager(t)
	requested := []string{}
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		requested = append(requested, req.URL.Path)
		var body string
		switch req.URL.Path {
		case "/ocaml/ocaml.org/tree/main/data/releases":
			if req.Header.Get("Accept") != "application/json" {
				t.Fatalf("tree page requested as %q", req.Header.Get("Accept"))
			}
			body = ocamlTreePage("4.14.4.md", "6.0.2.md", "7.0.0-rc1.md", "6.1.1.md", "6.1.0.md")
		case "/ocaml/ocaml.org/main/data/releases/6.1.1.md":
			// Published ahead of its announcement: not flagged yet, so skipped.
			body = "---\nkind: compiler\nversion: 6.1.1\nis_latest: false\n---\n"
		case "/ocaml/ocaml.org/main/data/releases/6.1.0.md":
			body = "---\nkind: compiler\nversion: 6.1.0\nis_latest: true\n---\n"
		default:
			t.Fatalf("unexpected metadata fetch %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if got, err := m.ocamlLatest(); got != "6.1.0" || err != nil {
		t.Fatalf("latest=%q,%v", got, err)
	}
	if len(requested) != 3 {
		t.Fatalf("metadata requests=%v", requested)
	}
	if packageChannel("ocaml") != "stable" {
		t.Fatal("OCaml must follow the stable channel, not LTS")
	}
}

func TestOCamlLatestRequiresValidMetadata(t *testing.T) {
	for _, tc := range []struct {
		body    string
		status  int
		wantErr bool
	}{
		{"---\nkind: compiler\nversion: 6.1.0\nis_latest: true\n---\n", 200, false},
		{"---\nkind: compiler\nversion: 6.1.0\nis_latest: maybe\n---\n", 200, true},
		{"---\nkind: compiler\nversion: 6.1.0\n---\n", 200, true},
		{"not metadata", 200, true},
		{"unavailable", 404, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			m := inventoryManager(t)
			m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				body, status := tc.body, tc.status
				if req.URL.Path == "/ocaml/ocaml.org/tree/main/data/releases" {
					body, status = ocamlTreePage("6.1.0.md"), 200
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			got, err := m.ocamlLatest()
			if (err != nil) != tc.wantErr || (!tc.wantErr && got != "6.1.0") {
				t.Fatalf("latest=%q,%v; wantErr=%v", got, err, tc.wantErr)
			}
		})
	}
}

func ocamlTreePage(names ...string) string {
	var items []string
	for _, name := range names {
		items = append(items, fmt.Sprintf(`{"name":%q,"path":"data/releases/%s","contentType":"file"}`, name, name))
	}
	return `{"meta":{"title":"releases"},"payload":{"codeViewTreeRoute":{"path":"data/releases","tree":{"items":[` + strings.Join(items, ",") + `]}}}}`
}

// The tree page is a web route; if its shape changes, the contents API is used.
func TestOCamlReleaseListFallsBackToTheContentsAPI(t *testing.T) {
	m := inventoryManager(t)
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch req.URL.String() {
		case "https://github.com/ocaml/ocaml.org/tree/main/data/releases":
			body = "<html>changed</html>"
		case "https://api.github.com/repos/ocaml/ocaml.org/contents/data/releases":
			body = `[{"name":"6.1.0.md","type":"file"}]`
		default:
			body = "---\nkind: compiler\nversion: 6.1.0\nis_latest: true\n---\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if got, err := m.ocamlLatest(); got != "6.1.0" || err != nil {
		t.Fatalf("latest=%q,%v", got, err)
	}
}
