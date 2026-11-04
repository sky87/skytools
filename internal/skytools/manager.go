package skytools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type manager struct {
	ctx         context.Context
	home        string
	env         []string
	out, errOut io.Writer
	client      *http.Client
}

func newManager(ctx context.Context, out, errOut io.Writer) (*manager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(home) {
		return nil, fmt.Errorf("HOME must be an absolute path")
	}
	m := &manager{ctx: ctx, home: filepath.Clean(home), out: out, errOut: errOut, client: &http.Client{Timeout: 15 * time.Minute}}
	m.env = append([]string{}, os.Environ()...)
	for key, value := range map[string]string{
		"HOME": m.home, "NVM_DIR": m.at(".nvm"), "RUSTUP_HOME": m.at(".rustup"),
		"CARGO_HOME": m.at(".cargo"), "BUN_INSTALL": m.at(".bun"), "DOTNET_ROOT": m.at(".dotnet"),
		"GOROOT": m.at(".go/current"), "GOPATH": m.at("go"), "JAVA_HOME": m.at(".jdk/current"),
		"FZF_HOME": m.at(".fzf"), "OPAMROOT": m.at(".opam"), "ELAN_HOME": m.at(".elan"),
		"PNPM_HOME": m.at(".local/share/pnpm"), "UV_INSTALL_DIR": m.at(".local/bin"),
		"UV_NO_MODIFY_PATH": "1", "DOTNET_CLI_TELEMETRY_OPTOUT": "1", "PROFILE": "/dev/null",
		"RUSTUP_INIT_SKIP_PATH_CHECK": "yes",
	} {
		m.env = setEnv(m.env, key, value)
	}
	paths := []string{".local/bin", ".opencode/bin", ".local/share/pnpm/bin", ".local/share/pnpm", ".nvm/current/bin", ".cargo/bin", ".bun/bin", ".dotnet", ".go/current/bin", "go/bin", ".jdk/current/bin", ".fzf/bin", ".opam/default/bin", ".elan/bin"}
	for i := range paths {
		paths[i] = m.at(paths[i])
	}
	m.env = setEnv(m.env, "PATH", strings.Join(paths, string(os.PathListSeparator))+string(os.PathListSeparator)+os.Getenv("PATH"))
	return m, nil
}

func setEnv(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}

func (m *manager) lookup(name string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		return name, nil
	}
	search := ""
	for _, e := range m.env {
		if strings.HasPrefix(e, "PATH=") {
			search = strings.TrimPrefix(e, "PATH=")
		}
	}
	for _, dir := range filepath.SplitList(search) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if executable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("required command %q is missing; install the prerequisites listed in README.md", name)
}

func (m *manager) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	name, err := m.lookup(args[0])
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args[1:]...)
	cmd.Env, cmd.Dir = m.env, m.home
	// Kill the entire process group on cancellation, including installer children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	return cmd, nil
}

func (m *manager) run(args ...string) error {
	cmd, err := m.command(m.ctx, args...)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = m.out, m.errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	return nil
}

func (m *manager) outputContext(ctx context.Context, args ...string) (string, error) {
	cmd, err := m.command(ctx, args...)
	if err != nil {
		return "", err
	}
	data, err := cmd.CombinedOutput()
	return string(data), err
}

func (m *manager) output(args ...string) (string, error) { return m.outputContext(m.ctx, args...) }
func (m *manager) bash(script string, args ...string) error {
	return m.run(append([]string{"bash", "-e", "-o", "pipefail", "-c", script, "skytools"}, args...)...)
}
func (m *manager) log(format string, args ...any) {
	fmt.Fprintf(m.out, "skytools: "+format+"\n", args...)
}

func (m *manager) withLock(fn func() error) error {
	if err := os.MkdirAll(m.at(".local/bin"), 0755); err != nil {
		return err
	}
	dir := m.at(".local/state/skytools")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func atomicWrite(name string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".skytools-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}

func link(target, name string) error {
	if info, err := os.Lstat(name); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("refusing to replace non-symlink %s", name)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(filepath.Dir(name), ".skytools-link-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "link")
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
