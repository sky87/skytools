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
	if !executable(filepath.Join(target, "bin/go")) || nativeGoVersion(target) != version {
		dir, err := m.archive("https://go.dev/dl/"+name, checksum, root)
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		source := filepath.Join(dir, "contents/go")
		if !executable(filepath.Join(source, "bin/go")) || nativeGoVersion(source) != version {
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

func (m *manager) installJDK() error {
	major, err := m.correttoMajor()
	if err != nil {
		return err
	}
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
	if !correttoJDK(target) {
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
		if !correttoJDK(source) {
			return fmt.Errorf("unexpected Corretto archive layout")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := os.Rename(source, target); err != nil {
			return err
		}
	}
	return link(target, m.at(".jdk/current"))
}

func (m *manager) ensureJDK() error {
	if correttoJDK(m.at(".jdk/current")) {
		return nil
	}
	return m.installJDK()
}

func (m *manager) installGH() error {
	tag, err := m.latestTag("cli/cli")
	if err != nil {
		return err
	}
	version := strings.TrimPrefix(tag, "v")
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

func (m *manager) installFZFBinary(tag string) error {
	version := strings.TrimPrefix(tag, "v")
	if !releaseVersion.MatchString(version) {
		return fmt.Errorf("invalid fzf release %q", tag)
	}
	name := fmt.Sprintf("fzf-%s-%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	base := "https://github.com/junegunn/fzf/releases/download/" + tag + "/"
	checksums, err := m.fetch(base + "fzf_" + version + "_checksums.txt")
	if err != nil {
		return err
	}
	checksum, err := checksumFor(checksums, name)
	if err != nil {
		return err
	}
	dir, err := m.archive(base+name, checksum, m.at(".fzf"))
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "contents/fzf")
	if !executable(source) {
		return fmt.Errorf("unexpected fzf archive layout")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return atomicWrite(m.at(".fzf/bin/fzf"), data, 0755)
}
