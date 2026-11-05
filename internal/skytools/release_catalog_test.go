package skytools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestVersionComparisonDoesNotInventUpdates(t *testing.T) {
	for _, tc := range []struct {
		a, b  string
		want  int
		known bool
	}{
		{"v1.9.0", "1.10.0", -1, true}, {"go1.27.3", "1.27.3", 0, true}, {"5.5.2", "4.14.4", 1, true},
		{"bun-v1.3.0", "1.4.0", -1, true},
		{"25.0.5.0.9", "25.0.5.0.10", -1, true}, {"25.0.5", "25.0.5.0.10", 0, false},
		{"1.2.3+build4", "1.2.3+build9", 0, true}, {"1.2.3-rc.1", "1.2.3", 0, false}, {"", "1.2.3", 0, false},
	} {
		got, known := compareVersions(tc.a, tc.b)
		if got != tc.want || known != tc.known {
			t.Errorf("compare %q %q =%d,%v", tc.a, tc.b, got, known)
		}
	}
}

func TestCatalogMatchesEveryPrimaryPackagePolicy(t *testing.T) {
	responses := map[string]string{
		"https://nodejs.org/dist/index.json":                                              `[{"version":"v99.1.0","lts":false},{"version":"v28.3.0","lts":"Next"},{"version":"v26.1.0","lts":"Old"}]`,
		"https://builds.dotnet.microsoft.com/dotnet/release-metadata/releases-index.json": `{"releases-index":[{"release-type":"sts","support-phase":"active","latest-sdk":"99.0.100"},{"release-type":"lts","support-phase":"active","latest-sdk":"12.0.101"},{"release-type":"lts","support-phase":"eol","latest-sdk":"98.0.100"}]}`,
		"https://api.adoptium.net/v3/info/available_releases":                             `{"available_lts_releases":[25,29],"most_recent_lts":29}`,
		"https://api.github.com/repos/corretto/corretto-29/releases/latest":               `{"tag_name":"29.0.1.0.4"}`,
		"https://api.github.com/repos/ocaml/ocaml.org/contents/data/releases":             `[{"name":"6.0.1.md","type":"file"}]`,
		"https://raw.githubusercontent.com/ocaml/ocaml.org/main/data/releases/6.0.1.md":   "---\nkind: compiler\nversion: 6.0.1\nis_lts: true\n---\n",
		"https://static.rust-lang.org/dist/channel-rust-stable.toml":                      "[pkg.rust]\nversion = \"1.99.1 (hash date)\"\n",
		"https://downloads.claude.ai/claude-code-releases/stable":                         "2.1.311\n",
		"https://releases.openai.com/codex/channels/latest":                               `{"tag_name":"rust-v0.166.0"}`,
		tailscalePackages + "?mode=json":                                                  `{"TarballsVersion":"1.104.5","Tarballs":{}}`,
		"https://registry.npmjs.org/opencode-ai/latest":                                   `{"version":"1.19.5"}`,
		"https://registry.npmjs.org/@earendil-works/pi-coding-agent/latest":               `{"version":"1.5.0"}`,
		"https://registry.npmjs.org/playwright/latest":                                    `{"version":"1.64.1"}`,
		"https://registry.npmjs.org/@playwright/cli/latest":                               `{"version":"0.1.23"}`,
	}
	responses["https://go.dev/dl/?mode=json"] = fmt.Sprintf(`[{"version":"go1.28.1","stable":true,"files":[{"filename":"go1.28.1.%s-%s.tar.gz","os":%q,"arch":%q,"kind":"archive","sha256":%q}]}]`, runtime.GOOS, runtime.GOARCH, runtime.GOOS, runtime.GOARCH, strings.Repeat("a", 64))
	for _, repo := range []string{"astral-sh/uv", "oven-sh/bun", "junegunn/fzf", "cli/cli", "leanprover/lean4", "aristocratos/btop"} {
		responses["https://api.github.com/repos/"+repo+"/releases/latest"] = `{"tag_name":"v1.2.3","draft":false,"prerelease":false}`
	}
	responses["https://api.github.com/repos/oven-sh/bun/releases/latest"] = `{"tag_name":"bun-v1.2.3","draft":false,"prerelease":false}`
	want := map[string]string{"uv": "1.2.3", "rust": "1.99.1", "node": "28.3.0", "bun": "1.2.3", "dotnet": "12.0.101", "go": "1.28.1", "jdk": "29.0.1.0.4", "fzf": "1.2.3", "gh": "1.2.3", "ocaml": "6.0.1", "lean": "1.2.3", "claude": "2.1.311", "codex": "0.166.0", "opencode": "1.19.5", "pi": "1.5.0", "btop": "1.2.3"}
	want["playwright"], want["playwright-cli"], want["tailscale"] = "1.64.1", "0.1.23", "1.104.5"
	m := testManager(t)
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		body, ok := responses[req.URL.String()]
		if !ok {
			return nil, fmt.Errorf("unexpected endpoint %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	for _, pkg := range m.tools() {
		t.Run(pkg.name, func(t *testing.T) {
			info, err := m.latestVersion(pkg.name)
			if err != nil || info.Version != want[pkg.name] || info.Channel != packageChannel(pkg.name) {
				t.Fatalf("%+v, %v", info, err)
			}
		})
	}
}

func TestCatalogFailureAndCancellationStayUnknown(t *testing.T) {
	m := testManager(t)
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("outage")), Header: http.Header{}}, nil
	})
	if _, err := m.latestVersion("uv"); err == nil {
		t.Fatal("outage reported a version")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.ctx = ctx
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) { return nil, req.Context().Err() })
	if _, err := m.latestVersion("uv"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestLiveReleaseCatalog(t *testing.T) {
	if os.Getenv("SKYTOOLS_LIVE_METADATA") != "1" {
		t.Skip("opt-in upstream metadata check")
	}
	m := testManager(t)
	for _, pkg := range m.tools() {
		t.Run(pkg.name, func(t *testing.T) {
			info, err := m.latestVersion(pkg.name)
			if err != nil {
				t.Fatal(err)
			}
			t.Log(pkg.name, info.Version, info.Channel)
		})
	}
}
