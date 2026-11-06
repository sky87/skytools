package skytools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type ReleaseInfo struct{ Version, Channel, SourceURL string }

// Metadata checks are separate from installation downloads: bounded, cancellable,
// and never retried for minutes while the dashboard is waiting for an answer.
func (m *manager) metadata(url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "skytools")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err == nil && len(data) > 4*1024*1024 {
		err = fmt.Errorf("release metadata exceeds 4 MiB")
	}
	return data, err
}

func (m *manager) metadataJSON(url string, value any) error {
	data, err := m.metadata(url)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

var numericVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9]+)*$`)

func cleanVersion(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"rust-v", "bun-v", "Corretto-", "go", "v"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimPrefix(value, prefix)
			break
		}
	}
	value = strings.SplitN(value, "+", 2)[0]
	return value
}

// Unknown and incomplete build information never becomes an update alert.
func compareVersions(installed, latest string) (int, bool) {
	a, b := cleanVersion(installed), cleanVersion(latest)
	if !numericVersion.MatchString(a) || !numericVersion.MatchString(b) {
		return 0, false
	}
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < min(len(aa), len(bb)); i++ {
		x, e1 := strconv.ParseUint(aa[i], 10, 64)
		y, e2 := strconv.ParseUint(bb[i], 10, 64)
		if e1 != nil || e2 != nil {
			return 0, false
		}
		if x < y {
			return -1, true
		}
		if x > y {
			return 1, true
		}
	}
	if len(aa) != len(bb) {
		return 0, false
	}
	return 0, true
}

type githubRelease struct {
	Tag               string `json:"tag_name"`
	Draft, Prerelease bool
	Assets            []struct {
		Name, Digest string
		URL          string `json:"browser_download_url"`
	}
}

func (m *manager) githubRelease(repository string) (githubRelease, error) {
	var release githubRelease
	err := m.metadataJSON("https://api.github.com/repos/"+repository+"/releases/latest", &release)
	if err == nil && (release.Draft || release.Prerelease || !numericVersion.MatchString(cleanVersion(release.Tag))) {
		err = fmt.Errorf("invalid stable release for %s", repository)
	}
	return release, err
}

func (m *manager) latestVersion(name string) (ReleaseInfo, error) {
	ctx, cancel := context.WithTimeout(m.ctx, 12*time.Second)
	defer cancel()
	worker := *m
	worker.ctx = ctx
	return worker.lookupLatestVersion(name)
}

func (m *manager) lookupLatestVersion(name string) (ReleaseInfo, error) {
	info := ReleaseInfo{Channel: packageChannel(name)}
	var err error
	repositories := map[string]string{"uv": "astral-sh/uv", "bun": "oven-sh/bun", "fzf": "junegunn/fzf", "gh": "cli/cli", "lean": "leanprover/lean4", "btop": "aristocratos/btop"}
	if repository := repositories[name]; repository != "" {
		info.SourceURL = "https://api.github.com/repos/" + repository + "/releases/latest"
		var release githubRelease
		release, err = m.githubRelease(repository)
		info.Version = cleanVersion(release.Tag)
	} else {
		switch name {
		case "node":
			info.SourceURL = "https://nodejs.org/dist/index.json"
			var releases []struct {
				Version string
				LTS     json.RawMessage
			}
			if err = m.metadataJSON(info.SourceURL, &releases); err == nil {
				for _, r := range releases {
					var lts string
					if json.Unmarshal(r.LTS, &lts) == nil && lts != "" && numericVersion.MatchString(cleanVersion(r.Version)) {
						if c, ok := compareVersions(info.Version, r.Version); info.Version == "" || ok && c < 0 {
							info.Version = cleanVersion(r.Version)
						}
					}
				}
			}
		case "dotnet":
			info.SourceURL = "https://builds.dotnet.microsoft.com/dotnet/release-metadata/releases-index.json"
			var metadata struct {
				Releases []struct {
					Type  string `json:"release-type"`
					Phase string `json:"support-phase"`
					SDK   string `json:"latest-sdk"`
				} `json:"releases-index"`
			}
			if err = m.metadataJSON(info.SourceURL, &metadata); err == nil {
				for _, r := range metadata.Releases {
					if r.Type == "lts" && (r.Phase == "active" || r.Phase == "maintenance") && numericVersion.MatchString(r.SDK) {
						if c, ok := compareVersions(info.Version, r.SDK); info.Version == "" || ok && c < 0 {
							info.Version = r.SDK
						}
					}
				}
			}
		case "jdk":
			var releases javaReleases
			if err = m.metadataJSON("https://api.adoptium.net/v3/info/available_releases", &releases); err == nil {
				var major string
				if major, err = latestJavaLTS(releases); err == nil {
					info.SourceURL = "https://api.github.com/repos/corretto/corretto-" + major + "/releases/latest"
					var release githubRelease
					release, err = m.githubRelease("corretto/corretto-" + major)
					info.Version = cleanVersion(release.Tag)
					if !strings.HasPrefix(info.Version, major+".") {
						err = fmt.Errorf("Corretto release does not match Java LTS %s", major)
					}
				}
			}
		case "ocaml":
			info.SourceURL = "https://github.com/ocaml/ocaml.org/tree/main/data/releases"
			info.Version, err = m.ocamlLatest()
		case "go":
			info.SourceURL = "https://go.dev/dl/?mode=json"
			var releases []goRelease
			if err = m.metadataJSON(info.SourceURL, &releases); err == nil {
				info.Version, _, _, err = latestGo(releases, runtime.GOOS, runtime.GOARCH)
				info.Version = cleanVersion(info.Version)
			}
		case "rust":
			info.SourceURL = "https://static.rust-lang.org/dist/channel-rust-stable.toml"
			var data []byte
			if data, err = m.metadata(info.SourceURL); err == nil {
				section := false
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "[") {
						section = line == "[pkg.rust]"
					}
					if section && strings.HasPrefix(line, "version = ") {
						fields := strings.Fields(strings.Trim(strings.TrimPrefix(line, "version = "), "\""))
						if len(fields) > 0 {
							info.Version = fields[0]
						}
						break
					}
				}
			}
		case "claude":
			info.SourceURL = "https://downloads.claude.ai/claude-code-releases/latest"
			var data []byte
			data, err = m.metadata(info.SourceURL)
			info.Version = strings.TrimSpace(string(data))
		case "codex":
			info.SourceURL = "https://releases.openai.com/codex/channels/latest"
			var release githubRelease
			err = m.metadataJSON(info.SourceURL, &release)
			info.Version = cleanVersion(release.Tag)
		case "tailscale":
			info.SourceURL = tailscalePackages + "?mode=json"
			var index tailscaleIndex
			err = m.metadataJSON(info.SourceURL, &index)
			info.Version = index.TarballsVersion
		case "opencode":
			// The endpoint the official OpenCode 2 installer resolves its version from.
			info.SourceURL = "https://opencode.ai/update/api/latest/cli/npm"
			var latest struct {
				Channel, Version string
				Active           bool
			}
			if err = m.metadataJSON(info.SourceURL, &latest); err == nil {
				if latest.Channel != "latest" || !latest.Active || !releaseVersion.MatchString(latest.Version) {
					err = fmt.Errorf("OpenCode reports no active latest release")
				}
				info.Version = latest.Version
			}
		case "pi", "playwright", "playwright-cli":
			pkg := "@earendil-works/pi-coding-agent"
			if name == "playwright" {
				pkg = "playwright"
			}
			if name == "playwright-cli" {
				pkg = "@playwright/cli"
			}
			info.SourceURL = "https://registry.npmjs.org/" + pkg + "/latest"
			var metadata struct{ Version string }
			err = m.metadataJSON(info.SourceURL, &metadata)
			info.Version = metadata.Version
		default:
			err = fmt.Errorf("no release policy for %s", name)
		}
	}
	if err == nil && !numericVersion.MatchString(info.Version) {
		err = fmt.Errorf("no valid %s %s version in upstream metadata", info.Channel, name)
	}
	return info, err
}
