package skytools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func (m *manager) installBtop() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("btop binary installation currently supports Linux only")
	}
	release, err := m.githubRelease("aristocratos/btop")
	if err != nil {
		return err
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		return fmt.Errorf("unsupported btop architecture %s", runtime.GOARCH)
	}
	name := "btop-" + arch + "-unknown-linux-musl.tar.gz"
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		url := "https://github.com/aristocratos/btop/releases/download/" + release.Tag + "/" + name
		checksum := strings.TrimPrefix(asset.Digest, "sha256:")
		if asset.URL != url || !validChecksum(checksum) {
			return fmt.Errorf("btop release has no valid SHA-256 digest for %s", name)
		}
		dir, err := m.archive(url, checksum, m.at(".local/state/skytools"))
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		source := filepath.Join(dir, "contents/btop/bin/btop")
		if !executable(source) {
			return fmt.Errorf("unexpected btop archive layout")
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		return atomicWrite(m.at(".local/bin/btop"), data, 0755)
	}
	return fmt.Errorf("btop release has no binary for %s", runtime.GOARCH)
}
