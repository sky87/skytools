package skytools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type javaReleases struct {
	AvailableLTS  []int `json:"available_lts_releases"`
	MostRecentLTS int   `json:"most_recent_lts"`
}

func latestJavaLTS(releases javaReleases) (string, error) {
	latest := 0
	for _, major := range releases.AvailableLTS {
		if major > latest {
			latest = major
		}
	}
	if latest <= 0 || latest != releases.MostRecentLTS {
		return "", fmt.Errorf("invalid Java LTS release metadata")
	}
	return strconv.Itoa(latest), nil
}

func (m *manager) correttoMajor() (string, error) {
	// Adoptium reports OpenJDK's release classification. Downloads and SHA-256
	// verification still come exclusively from Amazon Corretto, preserving the
	// selected vendor. An unavailable Corretto release is an error, never a
	// silent vendor or feature-version fallback.
	var releases javaReleases
	if err := m.json("https://api.adoptium.net/v3/info/available_releases", &releases); err != nil {
		return "", err
	}
	return latestJavaLTS(releases)
}

type ocamlReleaseFile struct{ Name, Type string }

// ocamlReleaseFiles lists data/releases through github.com's tree page, which
// answers in JSON when asked, instead of the rate-limited contents API (see
// github.go). The API remains the fallback if the page's shape changes.
func (m *manager) ocamlReleaseFiles() ([]ocamlReleaseFile, error) {
	var page struct {
		Payload struct {
			Route struct {
				Tree struct {
					Items []struct{ Name, ContentType string }
				}
			} `json:"codeViewTreeRoute"`
		}
	}
	data, err := m.metadataAccept("https://github.com/ocaml/ocaml.org/tree/main/data/releases", "application/json")
	if err == nil {
		err = json.Unmarshal(data, &page)
	}
	if err == nil && len(page.Payload.Route.Tree.Items) > 0 {
		var files []ocamlReleaseFile
		for _, item := range page.Payload.Route.Tree.Items {
			files = append(files, ocamlReleaseFile{item.Name, item.ContentType})
		}
		return files, nil
	}
	var files []ocamlReleaseFile
	if apiErr := m.metadataJSON("https://api.github.com/repos/ocaml/ocaml.org/contents/data/releases", &files); apiErr != nil {
		return nil, fmt.Errorf("list OCaml releases: %v (GitHub API fallback: %w)", err, apiErr)
	}
	return files, nil
}

// ocamlLatest reads the same release frontmatter used to build ocaml.org and
// returns the release flagged is_latest. Releases are read newest first, so the
// usual case costs one fetch; a newer file that is not flagged yet is skipped.
func (m *manager) ocamlLatest() (string, error) {
	files, err := m.ocamlReleaseFiles()
	if err != nil {
		return "", err
	}
	if len(files) >= 1000 {
		return "", fmt.Errorf("OCaml release metadata listing may be truncated")
	}
	var versions []string
	for _, file := range files {
		version := strings.TrimSuffix(file.Name, ".md")
		if file.Type == "file" && strings.HasSuffix(file.Name, ".md") && releaseVersion.MatchString(version) {
			if _, err := latestStableVersion(version); err != nil {
				return "", err
			}
			versions = append(versions, version)
		}
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("no stable OCaml release metadata")
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i] == versions[j] {
			return false
		}
		newest, _ := latestStableVersion(versions[i] + " " + versions[j])
		return newest == versions[i]
	})
	for _, version := range versions {
		data, err := m.fetch("https://raw.githubusercontent.com/ocaml/ocaml.org/main/data/releases/" + version + ".md")
		if err != nil {
			return "", err
		}
		latest, err := ocamlReleaseLatest(string(data), version)
		if err != nil {
			return "", err
		}
		if latest {
			return version, nil
		}
	}
	return "", fmt.Errorf("OCaml release metadata flags no release as latest")
}

func ocamlReleaseLatest(data, expected string) (bool, error) {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	if !strings.HasPrefix(data, "---\n") {
		return false, fmt.Errorf("invalid OCaml %s release frontmatter", expected)
	}
	end := strings.Index(data[4:], "\n---")
	if end < 0 {
		return false, fmt.Errorf("invalid OCaml %s release frontmatter", expected)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(data[4:4+end], "\n") {
		// Only top-level scalar classification keys are relevant. Indented intro
		// and highlights text can contain arbitrary Markdown and YAML punctuation.
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			fields[parts[0]] = strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		}
	}
	if fields["kind"] != "compiler" || fields["version"] != expected {
		return false, fmt.Errorf("invalid OCaml %s release classification", expected)
	}
	if value := fields["is_latest"]; value != "" && value != "true" && value != "false" {
		return false, fmt.Errorf("invalid OCaml %s is_latest flag", expected)
	}
	return fields["is_latest"] == "true", nil
}
