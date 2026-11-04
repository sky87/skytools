package skytools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var goVersion = regexp.MustCompile(`^go[0-9]+\.[0-9]+(\.[0-9]+)?$`)

type goRelease struct {
	Version string
	Stable  bool
	Files   []struct{ Filename, OS, Arch, Kind, SHA256 string }
}

func latestGo(releases []goRelease, system, arch string) (string, string, string, error) {
	for _, release := range releases {
		if !release.Stable || !goVersion.MatchString(release.Version) {
			continue
		}
		for _, file := range release.Files {
			if file.OS == system && file.Arch == arch && file.Kind == "archive" &&
				file.Filename == release.Version+"."+system+"-"+arch+".tar.gz" && validChecksum(file.SHA256) {
				return release.Version, file.Filename, file.SHA256, nil
			}
		}
	}
	return "", "", "", fmt.Errorf("no stable Go archive for %s/%s", system, arch)
}

func (m *manager) installGo() error {
	var releases []goRelease
	if err := m.json("https://go.dev/dl/?mode=json", &releases); err != nil {
		return err
	}
	version, name, checksum, err := latestGo(releases, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	root := m.at(".go/releases")
	target := filepath.Join(root, version)
	if !executable(filepath.Join(target, "bin/go")) {
		dir, err := m.archive("https://go.dev/dl/"+name, checksum, root)
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		source := filepath.Join(dir, "contents/go")
		if !executable(filepath.Join(source, "bin/go")) {
			return fmt.Errorf("unexpected Go archive layout")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := os.Rename(source, target); err != nil {
			return err
		}
	}
	return link(target, m.at(".go/current"))
}

func correttoAsset(system, arch, major string) (string, error) {
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(major) {
		return "", fmt.Errorf("invalid Corretto major %q", major)
	}
	architecture := map[string]string{"amd64": "x64", "arm64": "aarch64"}[arch]
	if architecture == "" {
		return "", fmt.Errorf("unsupported Corretto architecture %s", arch)
	}
	platform := map[string]string{"linux": "linux", "darwin": "macos"}[system]
	if platform == "" {
		return "", fmt.Errorf("unsupported Corretto OS %s", system)
	}
	return fmt.Sprintf("amazon-corretto-%s-%s-%s-jdk.tar.gz", major, architecture, platform), nil
}

func (m *manager) correttoMajor() string {
	if value := os.Getenv("SKY_TOOLS_CORRETTO_VERSION"); value != "" {
		return value
	}
	return "25"
}

func (m *manager) installJDK() error {
	major := m.correttoMajor()
	name, err := correttoAsset(runtime.GOOS, runtime.GOARCH, major)
	if err != nil {
		return err
	}
	data, err := m.fetch("https://corretto.aws/downloads/latest_sha256/" + name)
	if err != nil {
		return err
	}
	checksum := strings.TrimSpace(string(data))
	if !validChecksum(checksum) {
		return fmt.Errorf("invalid Corretto checksum")
	}
	root := m.at(".jdk/releases")
	target := filepath.Join(root, "corretto-"+major+"-"+checksum[:16])
	if !executable(filepath.Join(target, "bin/java")) || !executable(filepath.Join(target, "bin/javac")) {
		dir, err := m.archive("https://corretto.aws/downloads/latest/"+name, checksum, root)
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		entries, err := os.ReadDir(filepath.Join(dir, "contents"))
		if err != nil {
			return err
		}
		if len(entries) != 1 {
			return fmt.Errorf("unexpected Corretto archive layout")
		}
		source := filepath.Join(dir, "contents", entries[0].Name())
		if runtime.GOOS == "darwin" {
			source = filepath.Join(source, "Contents/Home")
		}
		if !executable(filepath.Join(source, "bin/java")) || !executable(filepath.Join(source, "bin/javac")) {
			return fmt.Errorf("unexpected Corretto archive layout")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := os.Rename(source, target); err != nil {
			return err
		}
	}
	if err := atomicWrite(filepath.Join(target, ".sky-tools-provider"), []byte("amazon-corretto-"+major+"\n"), 0644); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(target, ".sky-tools-sha256"), []byte(checksum+"\n"), 0644); err != nil {
		return err
	}
	return link(target, m.at(".jdk/current"))
}

func (m *manager) ensureJDK() error {
	provider, _ := os.ReadFile(m.at(".jdk/current/.sky-tools-provider"))
	if executable(m.at(".jdk/current/bin/java")) && executable(m.at(".jdk/current/bin/javac")) && strings.TrimSpace(string(provider)) == "amazon-corretto-"+m.correttoMajor() {
		return nil
	}
	return m.installJDK()
}

func (m *manager) installGH() error {
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := m.json("https://api.github.com/repos/cli/cli/releases/latest", &release); err != nil {
		return err
	}
	version := strings.TrimPrefix(release.TagName, "v")
	if !releaseVersion.MatchString(version) {
		return fmt.Errorf("invalid GitHub CLI release %q", release.TagName)
	}
	installed, _ := m.output(m.at(".local/bin/gh"), "--version")
	if strings.HasPrefix(installed, "gh version "+version+" ") {
		return nil
	}
	extension := ".tar.gz"
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macOS"
		extension = ".zip"
	}
	name := fmt.Sprintf("gh_%s_%s_%s%s", version, platform, runtime.GOARCH, extension)
	base := "https://github.com/cli/cli/releases/download/v" + version + "/"
	data, err := m.fetch(base + "gh_" + version + "_checksums.txt")
	if err != nil {
		return err
	}
	checksum, err := checksumFor(data, name)
	if err != nil {
		return err
	}
	dir, err := m.archive(base+name, checksum, m.at(".local/state/skytools"))
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "contents", strings.TrimSuffix(name, extension), "bin/gh")
	if !executable(source) {
		return fmt.Errorf("unexpected GitHub CLI archive layout")
	}
	data, err = os.ReadFile(source)
	if err != nil {
		return err
	}
	return atomicWrite(m.at(".local/bin/gh"), data, 0755)
}
