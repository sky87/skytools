package skytools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const selfRepository = "sky87/skytools"

func selfUpdateAsset(system, architecture string) (string, error) {
	if (system != "linux" && system != "darwin") || (architecture != "amd64" && architecture != "arm64") {
		return "", fmt.Errorf("self-update is unsupported on %s/%s", system, architecture)
	}
	return "skytools_" + system + "_" + architecture, nil
}

// selfUpdate runs the latest release's install.sh on the running executable.
// The update logic lives in that script, not in this binary, so a fix to it
// reaches every installed skytools, including one whose own updater is broken.
// The script is verified against the same release's SHA256SUMS before it runs;
// it then verifies the new binary the same way, checks the version it reports,
// and replaces the executable atomically, keeping symlinks and permissions.
//
// path is the running executable, supplied separately so tests never modify
// their own test runner.
func (m *manager) selfUpdate(current, path string) error {
	if _, err := selfUpdateAsset(runtime.GOOS, runtime.GOARCH); err != nil {
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
	if info, err := existing.Stat(); err != nil {
		return err
	} else if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("not an executable file: %s", target)
	}
	// Hold a lock on the old executable for the whole update, so two updaters
	// never run at once. Nothing persistent is created.
	if err := syscall.Flock(int(existing.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return fmt.Errorf("another self-update is already running for %s", target)
		}
		return fmt.Errorf("lock skytools executable: %w", err)
	}
	defer syscall.Flock(int(existing.Fd()), syscall.LOCK_UN)

	releases := m.selfReleases()
	m.log("checking the latest skytools release on GitHub")
	sums, err := m.metadata(releases + "/latest/download/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("download the latest skytools checksums: %w", err)
	}
	script, err := m.metadata(releases + "/latest/download/install.sh")
	if err != nil {
		return fmt.Errorf("download the latest skytools install.sh: %w", err)
	}
	checksum, err := checksumFor(sums, "install.sh")
	if err != nil {
		return fmt.Errorf("the latest skytools release: %w", err)
	}
	if hash := sha256.Sum256(script); !strings.EqualFold(hex.EncodeToString(hash[:]), checksum) {
		return fmt.Errorf("SHA-256 mismatch for install.sh; not running it")
	}
	file, err := os.CreateTemp("", "skytools-install-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(script); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}

	worker := *m
	worker.env = setEnv(append([]string{}, m.env...), "SKYTOOLS_TARGET", target)
	worker.env = setEnv(worker.env, "SKYTOOLS_RELEASES", releases)
	cmd, err := worker.command(m.ctx, "sh", file.Name())
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = m.out, m.errOut
	err = cmd.Run()
	if cmd.Process != nil {
		// Cancellation kills the script before its own cleanup runs; its staged
		// file carries its process ID.
		staged, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".skytools-update-"+strconv.Itoa(cmd.Process.Pid)+"-*"))
		for _, name := range staged {
			os.Remove(name)
		}
	}
	if ctxErr := m.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return fmt.Errorf("install.sh failed (%w); skytools %s is unchanged at %s", err, current, target)
	}
	return nil
}

// selfReleases is where skytools releases are downloaded from. SKYTOOLS_RELEASES
// overrides it for mirrors and tests, and is passed on to install.sh.
func (m *manager) selfReleases() string {
	if releases := strings.TrimSuffix(m.environmentValue("SKYTOOLS_RELEASES"), "/"); releases != "" {
		return releases
	}
	return "https://github.com/" + selfRepository + "/releases"
}
