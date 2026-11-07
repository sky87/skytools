package skytools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const selfRepository = "sky87/skytools"

func selfUpdateAsset(system, architecture string) (string, error) {
	if (system != "linux" && system != "darwin") || (architecture != "amd64" && architecture != "arm64") {
		return "", fmt.Errorf("self-update is unsupported on %s/%s", system, architecture)
	}
	return "skytools_" + system + "_" + architecture, nil
}

// path is the running executable, supplied separately so tests never modify
// their own test runner. Resolve aliases and replace the real file, not its link.
func (m *manager) selfUpdate(current, path string) error {
	asset, err := selfUpdateAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("locate skytools executable: %w", err)
	}
	existing, err := os.Open(target)
	if err != nil {
		return fmt.Errorf("open skytools executable: %w", err)
	}
	defer existing.Close()
	before, err := existing.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("not an executable file: %s", target)
	}
	// Lock the existing inode without creating any persistent update state. A
	// second updater holding an old inode is rejected by the identity check below.
	if err := syscall.Flock(int(existing.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return fmt.Errorf("another self-update is already running for %s", target)
		}
		return fmt.Errorf("lock skytools executable: %w", err)
	}
	defer syscall.Flock(int(existing.Fd()), syscall.LOCK_UN)

	m.log("checking the latest skytools release on GitHub")
	release, err := m.githubRelease(selfRepository)
	if err != nil {
		return fmt.Errorf("check latest skytools release: %w", err)
	}
	if comparison, known := compareVersions(current, release.Tag); known && comparison >= 0 {
		if comparison == 0 {
			m.log("skytools %s is already up to date", release.Tag)
		} else {
			m.log("skytools %s is newer than the latest release %s; keeping it", current, release.Tag)
		}
		return nil
	}
	urls := map[string]string{asset: release.DownloadURL(asset), "SHA256SUMS": release.DownloadURL("SHA256SUMS")}
	checksums, err := m.metadata(urls["SHA256SUMS"])
	if err != nil {
		return fmt.Errorf("skytools release %s requires %s and SHA256SUMS: download checksums: %w", release.Tag, asset, err)
	}
	checksum, err := checksumFor(checksums, asset)
	if err != nil {
		return err
	}
	staged, err := os.CreateTemp(filepath.Dir(target), ".skytools-update-*")
	if err != nil {
		return fmt.Errorf("prepare self-update in %s: %w; the executable directory must be writable (use a user-owned copy or rebuild the container image)", filepath.Dir(target), err)
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	m.log("downloading skytools %s for %s/%s", release.Tag, runtime.GOOS, runtime.GOARCH)
	hash := sha256.New()
	if err := m.download(urls[asset], io.MultiWriter(staged, hash)); err != nil {
		return fmt.Errorf("download skytools: %w", err)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), checksum) {
		return fmt.Errorf("SHA-256 mismatch for %s", asset)
	}
	if err := staged.Chmod(before.Mode().Perm()); err != nil {
		return err
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	// Check both architecture compatibility and the embedded release version by
	// running the verified candidate before replacing the working executable.
	if err := m.validateSelfUpdate(staged.Name(), release.Tag); err != nil {
		return err
	}
	if err := m.ctx.Err(); err != nil {
		return err
	}
	after, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return fmt.Errorf("skytools executable changed during update; retry")
	}
	if err := os.Rename(staged.Name(), target); err != nil {
		return fmt.Errorf("replace skytools executable: %w", err)
	}
	m.log("updated skytools from %s to %s at %s; the next invocation uses the new version", current, release.Tag, target)
	return nil
}

func (m *manager) validateSelfUpdate(path, version string) error {
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	cmd, err := m.command(ctx, path, "version")
	if err != nil {
		return err
	}
	var output versionOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("validate downloaded skytools: %w", err)
	}
	text := strings.TrimSpace(output.String())
	if !strings.HasPrefix(text, "skytools ") || cleanVersion(strings.TrimPrefix(text, "skytools ")) != cleanVersion(version) {
		return fmt.Errorf("downloaded skytools does not report release %s", version)
	}
	return nil
}
