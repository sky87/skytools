package skytools

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Everything skytools needs from GitHub releases goes through this file: the
// dashboard's version checks, every installer, and self-update.
//
// GitHub's REST API allows only 60 unauthenticated requests an hour per IP
// address, and one dashboard session checks about 20 GitHub projects, so the
// API is not used in normal operation and no token is ever needed. github.com's
// ordinary web routes give the same answers:
//
//   - /releases/latest redirects to the latest release's tag. GitHub never
//     treats a draft or prerelease as the latest release.
//   - /releases/expanded_assets/<tag> lists each asset with the SHA-256 digest
//     GitHub computed when it was uploaded.
//   - /releases/download/<tag>/<asset> serves the asset itself.
//
// The first two are pages meant for browsers. If either stops yielding what
// skytools needs, the REST API is tried once as a fallback.

// githubRelease is a GitHub project's latest stable release.
type githubRelease struct {
	Repository string
	Tag        string // as published: v10.5.2, jq-1.8.2, bun-v1.3.0, 15.2.0
	Version    string // the tag's numeric version: 10.5.2, 1.8.2, 1.3.0, 15.2.0
}

// githubRelease finds the latest stable release of owner/name.
func (m *manager) githubRelease(repository string) (githubRelease, error) {
	tag, err := m.latestReleaseTag(repository)
	if err != nil {
		return githubRelease{}, err
	}
	release := githubRelease{Repository: repository, Tag: tag, Version: cleanVersion(tag)}
	if !numericVersion.MatchString(release.Version) {
		return githubRelease{}, fmt.Errorf("invalid stable release %q for %s", tag, repository)
	}
	return release, nil
}

// DownloadURL is where one of the release's assets is downloaded from.
func (r githubRelease) DownloadURL(asset string) string {
	return "https://github.com/" + r.Repository + "/releases/download/" + url.PathEscape(r.Tag) + "/" + url.PathEscape(asset)
}

// Digest is the SHA-256 GitHub recorded for one of the release's assets, for
// projects that publish no checksum file of their own.
func (m *manager) githubDigest(r githubRelease, asset string) (string, error) {
	page, err := m.metadata("https://github.com/" + r.Repository + "/releases/expanded_assets/" + url.PathEscape(r.Tag))
	if err == nil {
		if digest, ok := assetDigests(string(page))[asset]; ok {
			return digest, nil
		}
	}
	var api struct {
		Assets []struct{ Name, Digest string }
	}
	if apiErr := m.metadataJSON("https://api.github.com/repos/"+r.Repository+"/releases/tags/"+url.PathEscape(r.Tag), &api); apiErr == nil {
		for _, candidate := range api.Assets {
			checksum, ok := strings.CutPrefix(candidate.Digest, "sha256:")
			if candidate.Name == asset && ok && validChecksum(checksum) {
				return checksum, nil
			}
		}
	} else if err == nil {
		err = apiErr
	}
	if err != nil {
		return "", fmt.Errorf("%s release %s: no SHA-256 digest for %s: %w", r.Repository, r.Tag, asset, err)
	}
	return "", fmt.Errorf("%s release %s has no %s with a SHA-256 digest", r.Repository, r.Tag, asset)
}

// latestReleaseTag follows /releases/latest one hop without downloading a page.
func (m *manager) latestReleaseTag(repository string) (string, error) {
	latest := "https://github.com/" + repository + "/releases/latest"
	req, err := http.NewRequestWithContext(m.ctx, http.MethodGet, latest, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "skytools")
	client := *m.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return m.latestReleaseTagFromAPI(repository, err)
	}
	resp.Body.Close()
	location, err := resp.Location()
	if resp.StatusCode < 300 || resp.StatusCode > 399 || err != nil {
		return m.latestReleaseTagFromAPI(repository, fmt.Errorf("%s: HTTP %d without a release redirect", latest, resp.StatusCode))
	}
	prefix := "/" + repository + "/releases/tag/"
	tag := ""
	if location.Host == "github.com" && len(location.Path) > len(prefix) && strings.EqualFold(location.Path[:len(prefix)], prefix) {
		tag = location.Path[len(prefix):]
	}
	if tag == "" || strings.Contains(tag, "/") {
		// A repository without releases redirects to its release list.
		return "", fmt.Errorf("%s has no latest release (redirected to %s)", repository, location)
	}
	return tag, nil
}

func (m *manager) latestReleaseTagFromAPI(repository string, webErr error) (string, error) {
	var release struct {
		Tag               string `json:"tag_name"`
		Draft, Prerelease bool
	}
	if err := m.metadataJSON("https://api.github.com/repos/"+repository+"/releases/latest", &release); err != nil {
		return "", fmt.Errorf("%w (GitHub API fallback: %v)", webErr, err)
	}
	if release.Draft || release.Prerelease {
		return "", fmt.Errorf("invalid stable release for %s", repository)
	}
	return release.Tag, nil
}

var clipboardTag = regexp.MustCompile(`<clipboard-copy\b[^>]*>`)
var digestLabel = regexp.MustCompile(`\baria-label="Copy to clipboard digest for ([^"]+)"`)
var digestValue = regexp.MustCompile(`\bvalue="sha256:([0-9a-f]{64})"`)

// assetDigests reads the asset list page. Each digest's copy button names its
// asset, so a digest is never paired with a neighbouring asset by position. An
// asset listed with two different digests is dropped.
func assetDigests(page string) map[string]string {
	digests, conflicts := map[string]string{}, map[string]bool{}
	for _, tag := range clipboardTag.FindAllString(page, -1) {
		label, value := digestLabel.FindStringSubmatch(tag), digestValue.FindStringSubmatch(tag)
		if label == nil || value == nil {
			continue
		}
		name := html.UnescapeString(label[1])
		if previous, ok := digests[name]; ok && previous != value[1] {
			conflicts[name] = true
		}
		digests[name] = value[1]
	}
	for name := range conflicts {
		delete(digests, name)
	}
	return digests
}
