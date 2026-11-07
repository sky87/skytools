package skytools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Fixtures for github.com's web release routes (see github.go).

func redirectResponse(location string) *http.Response {
	return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader(""))}
}

func latestTagResponse(repository, tag string) *http.Response {
	return redirectResponse("https://github.com/" + repository + "/releases/tag/" + tag)
}

// latestRepository says which repository a /releases/latest request is for.
func latestRepository(req *http.Request) (string, bool) {
	if req.URL.Host != "github.com" || !strings.HasSuffix(req.URL.Path, "/releases/latest") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/"), "/releases/latest"), true
}

func digestOf(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// expandedAssetsPage mimics GitHub's asset list markup around each digest.
func expandedAssetsPage(repository, tag string, digests map[string]string) string {
	var page strings.Builder
	page.WriteString("<div data-view-component=\"true\">\n")
	for name, digest := range digests {
		escaped := html.EscapeString(name)
		fmt.Fprintf(&page, `<a href="/%s/releases/download/%s/%s" rel="nofollow"><span class="text-bold">%s</span></a>
<span class="Truncate-text">sha256:%s</span>
<clipboard-copy id="clipboard-button-sha256:%s" aria-label="Copy to clipboard digest for %s" type="button" value="sha256:%s" class="Button--invisible"></clipboard-copy>
`, repository, tag, escaped, escaped, digest, digest, escaped, digest)
	}
	page.WriteString("</div>\n")
	return page.String()
}

func TestLatestReleaseTagFollowsTheWebRedirectWithoutTheAPI(t *testing.T) {
	for _, tc := range []struct{ location, want string }{
		{"https://github.com/sharkdp/fd/releases/tag/v10.5.2", "v10.5.2"},
		{"https://github.com/SharkDP/FD/releases/tag/v10.5.2", "v10.5.2"},
		{"/sharkdp/fd/releases/tag/jq-1.8.2", "jq-1.8.2"},
		{"https://github.com/sharkdp/fd/releases", ""},
		{"https://github.com/sharkdp/fd/releases/tag/", ""},
		{"https://github.com/sharkdp/fd/releases/tag/v1/../../main", ""},
		{"https://example.invalid/sharkdp/fd/releases/tag/v10.5.2", ""},
		{"https://github.com/other/fd/releases/tag/v10.5.2", ""},
	} {
		t.Run(tc.location, func(t *testing.T) {
			m := testManager(t)
			m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != "https://github.com/sharkdp/fd/releases/latest" {
					return nil, fmt.Errorf("unexpected request %s", req.URL)
				}
				response := redirectResponse(tc.location)
				response.Request = req
				return response, nil
			})
			got, err := m.latestReleaseTag("sharkdp/fd")
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("tag = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestLatestReleaseTagFallsBackToTheAPIOnlyWhenTheWebFails(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"tag_name":"v10.5.2"}`, "v10.5.2"},
		{`{"tag_name":"v10.6.0","prerelease":true}`, ""},
		{`{"tag_name":"v10.6.0","draft":true}`, ""},
	} {
		m := testManager(t)
		m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() == "https://api.github.com/repos/sharkdp/fd/releases/latest" {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}
			return &http.Response{StatusCode: 429, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("slow down"))}, nil
		})
		if got, err := m.latestReleaseTag("sharkdp/fd"); got != tc.want || (err == nil) != (tc.want != "") {
			t.Fatalf("%s: tag = %q, %v", tc.body, got, err)
		}
	}
}

func TestReleaseDigestReadsTheAssetPageAndFallsBackToTheAPI(t *testing.T) {
	good := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	page := expandedAssetsPage("sharkdp/fd", "v10.5.2", map[string]string{"fd.tar.gz": good, "fd & more.zip": other})
	// A second, different digest for one asset makes that asset untrusted.
	page += `<clipboard-copy aria-label="Copy to clipboard digest for conflict.tar.gz" value="sha256:` + good + `"></clipboard-copy>`
	page += `<clipboard-copy aria-label="Copy to clipboard digest for conflict.tar.gz" value="sha256:` + other + `"></clipboard-copy>`
	apiCalls := 0
	m := testManager(t)
	m.client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		body := ""
		switch req.URL.String() {
		case "https://github.com/sharkdp/fd/releases/expanded_assets/v10.5.2":
			body = page
		case "https://api.github.com/repos/sharkdp/fd/releases/tags/v10.5.2":
			apiCalls++
			body = `{"assets":[{"name":"only-in-api.tar.gz","digest":"sha256:` + other + `"},{"name":"no-digest.tar.gz","digest":null}]}`
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	for _, tc := range []struct{ asset, want string }{
		{"fd.tar.gz", good}, {"fd & more.zip", other}, {"only-in-api.tar.gz", other},
		{"conflict.tar.gz", ""}, {"no-digest.tar.gz", ""}, {"absent.tar.gz", ""},
	} {
		before := apiCalls
		got, err := m.githubDigest(githubRelease{Repository: "sharkdp/fd", Tag: "v10.5.2", Version: "10.5.2"}, tc.asset)
		if got != tc.want || (err == nil) != (tc.want != "") {
			t.Fatalf("%s: digest = %q, %v", tc.asset, got, err)
		}
		if usedAPI := apiCalls > before; usedAPI != (tc.asset != "fd.tar.gz" && tc.asset != "fd & more.zip") {
			t.Fatalf("%s: API used = %v", tc.asset, usedAPI)
		}
	}
}
