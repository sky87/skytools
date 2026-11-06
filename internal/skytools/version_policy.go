package skytools

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// GitHub's latest release endpoint excludes drafts and prereleases. Validate
// both the flags and tag so unexpected metadata never becomes an installer URL.
func (m *manager) latestTag(repository string) (string, error) {
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := m.json("https://api.github.com/repos/"+repository+"/releases/latest", &release); err != nil {
		return "", err
	}
	if release.Draft || release.Prerelease || !releaseVersion.MatchString(strings.TrimPrefix(release.Tag, "v")) {
		return "", fmt.Errorf("invalid stable %s release %q", repository, release.Tag)
	}
	return release.Tag, nil
}

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

// ocamlLatest reads the same release frontmatter used to build ocaml.org and
// returns the release flagged is_latest. Releases are read newest first, so the
// usual case costs one fetch; a newer file that is not flagged yet is skipped.
func (m *manager) ocamlLatest() (string, error) {
	var files []ocamlReleaseFile
	if err := m.json("https://api.github.com/repos/ocaml/ocaml.org/contents/data/releases", &files); err != nil {
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
