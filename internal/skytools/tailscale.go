package skytools

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const tailscalePackages = "https://pkgs.tailscale.com/stable/"

type tailscaleIndex struct {
	TarballsVersion string
	Tarballs        map[string]string
}

var tailscaleTarball = regexp.MustCompile(`^tailscale_[0-9]+(\.[0-9]+)+_[a-z0-9]+\.tgz$`)

// installTailscale installs the client and daemon binaries only. The daemon needs
// a decision skytools cannot make for the user: root with a TUN device, or
// userspace networking. So nothing is started, enabled, or logged in here.
func (m *manager) installTailscale() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("tailscale binary installation currently supports Linux only")
	}
	var index tailscaleIndex
	if err := m.metadataJSON(tailscalePackages+"?mode=json", &index); err != nil {
		return err
	}
	name := index.Tarballs[runtime.GOARCH]
	if !tailscaleTarball.MatchString(name) {
		return fmt.Errorf("tailscale has no valid package for %s", runtime.GOARCH)
	}
	url := tailscalePackages + name
	published, err := m.metadata(url + ".sha256")
	if err != nil {
		return fmt.Errorf("download tailscale checksum: %w", err)
	}
	checksum := strings.TrimSpace(string(published))
	if !validChecksum(checksum) {
		return fmt.Errorf("tailscale publishes no valid SHA-256 checksum for %s", name)
	}
	dir, err := m.archive(url, checksum, m.at(".local/state/skytools"))
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	root := filepath.Join(dir, "contents", strings.TrimSuffix(name, ".tgz"))
	// Validate both before replacing either, so a bad archive cannot leave the
	// client and daemon at different versions.
	binaries := []string{"tailscaled", "tailscale"}
	for _, binary := range binaries {
		if !executable(filepath.Join(root, binary)) {
			return fmt.Errorf("unexpected tailscale archive layout: %s is missing", binary)
		}
	}
	for _, binary := range binaries {
		data, err := os.ReadFile(filepath.Join(root, binary))
		if err != nil {
			return err
		}
		if err := atomicWrite(m.at(".local/bin", binary), data, 0755); err != nil {
			return err
		}
	}
	return nil
}
