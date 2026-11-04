package skytools

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (m *manager) tools() []tool {
	version := func(label, path string, args ...string) versionCommand {
		return versionCommand{label, append([]string{m.at(path)}, args...)}
	}
	install := func(url, shell string, args ...string) func() error {
		return func() error { return m.installer(url, shell, args...) }
	}
	command := func(args ...string) func() error { return func() error { return m.run(args...) } }
	missing := func(path string, action func() error) func() error {
		return func() error {
			if executable(m.at(path)) {
				return nil
			}
			if err := action(); err != nil {
				return err
			}
			if !executable(m.at(path)) {
				return fmt.Errorf("installer succeeded but %s is missing", m.at(path))
			}
			return nil
		}
	}
	dotnet := install("https://dot.net/v1/dotnet-install.sh", "bash", "--install-dir", m.at(".dotnet"), "--channel", "LTS", "--no-path")
	codex := install("https://chatgpt.com/codex/install.sh", "sh", "--release", "latest")
	claude := install("https://claude.ai/install.sh", "bash", "stable")
	opencode := install("https://opencode.ai/install", "bash", "--no-modify-path")
	return []tool{
		{"uv", "uv (Python manager)", missing(".local/bin/uv", install("https://astral.sh/uv/install.sh", "sh")), command(m.at(".local/bin/uv"), "self", "update"), []versionCommand{version("uv", ".local/bin/uv", "--version")}},
		{"rust", "Rust / rustup", m.ensureRust, m.updateRust, []versionCommand{version("rust", ".cargo/bin/rustc", "--version"), version("rustup", ".cargo/bin/rustup", "--version")}},
		{"node", "nvm / Node.js LTS / pnpm", m.ensureNode, m.updateNode, []versionCommand{
			{"nvm", []string{"bash", "-c", `. "$NVM_DIR/nvm.sh"; nvm --version`}},
			version("node", ".local/bin/node", "--version"), version("npm", ".local/bin/npm", "--version"), version("pnpm", ".local/bin/pnpm", "--version"),
		}},
		{"bun", "Bun", missing(".bun/bin/bun", install("https://bun.sh/install", "bash")), command(m.at(".bun/bin/bun"), "upgrade"), []versionCommand{version("bun", ".bun/bin/bun", "--version")}},
		{"dotnet", ".NET LTS", func() error {
			if m.managedStatus("dotnet").Installed {
				return nil
			}
			return dotnet()
		}, dotnet, []versionCommand{version("dotnet", ".dotnet/dotnet", "--version")}},
		{"go", "Go stable", func() error {
			if m.managedStatus("go").Installed {
				return nil
			}
			return m.installGo()
		}, m.installGo, []versionCommand{version("go", ".go/current/bin/go", "version")}},
		{"jdk", "Amazon Corretto JDK", m.ensureJDK, m.installJDK, []versionCommand{version("java", ".jdk/current/bin/java", "-version"), version("javac", ".jdk/current/bin/javac", "-version")}},
		{"fzf", "fzf", missing(".fzf/bin/fzf", m.ensureFZF), m.updateFZF, []versionCommand{version("fzf", ".fzf/bin/fzf", "--version")}},
		{"gh", "GitHub CLI", missing(".local/bin/gh", m.installGH), m.installGH, []versionCommand{version("gh", ".local/bin/gh", "--version")}},
		{"ocaml", "opam / OCaml LTS", m.ensureOCaml, m.updateOCaml, []versionCommand{version("opam", ".local/bin/opam", "--version"), version("ocaml", ".local/bin/opam", "exec", "--switch=default", "--", "ocaml", "-version")}},
		{"lean", "elan / Lean 4 stable", m.ensureLean, m.updateLean, []versionCommand{version("elan", ".elan/bin/elan", "--version"), version("lean", ".elan/bin/elan", "toolchain", "list")}},
		{"claude", "Claude Code", missing(".local/bin/claude", claude), claude, []versionCommand{version("claude", ".local/bin/claude", "--version")}},
		{"codex", "Codex", missing(".local/bin/codex", codex), codex, []versionCommand{version("codex", ".local/bin/codex", "--version")}},
		{"opencode", "OpenCode", missing(".opencode/bin/opencode", opencode), opencode, []versionCommand{version("opencode", ".opencode/bin/opencode", "--version")}},
		{"pi", "pi coding agent", m.ensurePi, command("pnpm", "add", "--global", "--ignore-scripts", "@earendil-works/pi-coding-agent@latest"), []versionCommand{{"pi", []string{"pi", "--version"}}}},
		{"btop", "btop resource monitor", missing(".local/bin/btop", m.installBtop), m.installBtop, []versionCommand{version("btop", ".local/bin/btop", "--version")}},
	}
}

func (m *manager) ensureRust() error {
	if m.managedStatus("rust").Installed {
		return nil
	}
	if !executable(m.at(".cargo/bin/rustup")) {
		return m.installer("https://sh.rustup.rs", "sh", "-y", "--no-modify-path", "--default-toolchain", "stable")
	}
	if installed := m.installedRustToolchain(); installed != "" {
		return m.run(m.at(".cargo/bin/rustup"), "default", installed)
	}
	return m.run(m.at(".cargo/bin/rustup"), "default", "stable")
}

func (m *manager) updateRust() error {
	if err := m.run(m.at(".cargo/bin/rustup"), "update", "stable"); err != nil {
		return err
	}
	return m.run(m.at(".cargo/bin/rustup"), "default", "stable")
}

func (m *manager) installNVM() error {
	version, err := m.latestTag("nvm-sh/nvm")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.at(".nvm"), 0755); err != nil {
		return err
	}
	return m.installer("https://raw.githubusercontent.com/nvm-sh/nvm/"+version+"/install.sh", "bash")
}

func (m *manager) ensureNode() error {
	if m.managedStatus("node").Installed {
		return nil
	}
	if _, err := os.Stat(m.at(".nvm/nvm.sh")); os.IsNotExist(err) {
		if err := m.installNVM(); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return m.node(false)
}

func (m *manager) updateNode() error {
	if err := m.installNVM(); err != nil {
		return err
	}
	return m.node(true)
}

func (m *manager) node(update bool) error {
	script := `. "$NVM_DIR/nvm.sh"
if [ "$(nvm version 'lts/*')" = N/A ]; then nvm install --lts; fi
nvm alias default 'lts/*' >/dev/null
nvm use --silent default
if [ ! -x "$(dirname "$(nvm which current)")/pnpm" ]; then npm install --global pnpm@latest; fi
`
	if update {
		script = `. "$NVM_DIR/nvm.sh"
nvm install --lts --latest-npm
nvm alias default 'lts/*' >/dev/null
nvm use --silent default
npm install --global pnpm@latest
`
	}
	if err := m.bash(script); err != nil {
		return err
	}
	output, err := m.output("bash", "-e", "-c", `. "$NVM_DIR/nvm.sh"; nvm which default`)
	if err != nil {
		return err
	}
	node := strings.TrimSpace(output)
	if !filepath.IsAbs(node) || !strings.HasPrefix(node, m.at(".nvm/versions/node")+string(os.PathSeparator)) || !executable(node) {
		return fmt.Errorf("unexpected nvm Node path %q", node)
	}
	root := filepath.Dir(filepath.Dir(node))
	if err := link(root, m.at(".nvm/current")); err != nil {
		return err
	}
	for _, name := range []string{"node", "npm", "npx", "pnpm", "pnpx"} {
		target := m.at(".nvm/current/bin", name)
		if executable(target) {
			if err := link(target, m.at(".local/bin", name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *manager) ensureFZF() error {
	return m.installFZF()
}

func (m *manager) updateFZF() error { return m.installFZF() }

func (m *manager) installFZF() error {
	tag, err := m.latestTag("junegunn/fzf")
	if err != nil {
		return err
	}
	if _, err := os.Stat(m.at(".fzf/.git")); os.IsNotExist(err) {
		if err := m.run("git", "clone", "--depth", "1", "--branch", tag, "https://github.com/junegunn/fzf.git", m.at(".fzf")); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if err := m.run("git", "-C", m.at(".fzf"), "fetch", "--depth", "1", "origin", "tag", tag); err != nil {
			return err
		}
		if err := m.run("git", "-C", m.at(".fzf"), "checkout", "--detach", tag); err != nil {
			return err
		}
	}
	return m.installFZFBinary(tag)
}

func (m *manager) installOpam() error {
	// The official installer asks for a destination on stdin. Never accept its
	// system-wide default: install the binary in this user's ~/.local/bin.
	data, err := m.fetch("https://opam.ocaml.org/install.sh")
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "skytools-opam-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(name, data, 0600); err != nil {
		return err
	}
	cmd, err := m.command(m.ctx, "sh", name, "--no-backup")
	if err != nil {
		return err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(m.at(".local/bin")+"\n"), m.out, m.errOut
	return cmd.Run()
}

func latestStableVersion(output string) (string, error) {
	best := ""
	var bestParts [3]int
	for _, value := range strings.Fields(output) {
		if !releaseVersion.MatchString(value) {
			continue
		}
		var parts [3]int
		for i, number := range strings.Split(value, ".") {
			parsed, err := strconv.Atoi(number)
			if err != nil {
				return "", fmt.Errorf("invalid version %q", value)
			}
			parts[i] = parsed
		}
		newer := best == ""
		for i := range parts {
			if parts[i] != bestParts[i] {
				newer = parts[i] > bestParts[i]
				break
			}
		}
		if newer {
			best, bestParts = value, parts
		}
	}
	if best == "" {
		return "", fmt.Errorf("no stable version is available")
	}
	return best, nil
}

func latestCompiler(output string) (string, error) {
	version, err := latestStableVersion(output)
	if err != nil {
		return "", err
	}
	return "ocaml-base-compiler." + version, nil
}

func latestLeanToolchain(output string) (string, error) {
	var versions []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], "leanprover/lean4:v") {
			versions = append(versions, strings.TrimPrefix(fields[0], "leanprover/lean4:v"))
		}
	}
	version, err := latestStableVersion(strings.Join(versions, " "))
	if err != nil {
		return "", err
	}
	return "leanprover/lean4:v" + version, nil
}

func (m *manager) compiler() (string, error) {
	output, err := m.output(m.at(".local/bin/opam"), "show", "ocaml-base-compiler", "--all-versions", "--field=version")
	if err != nil {
		return "", fmt.Errorf("query OCaml compilers: %w: %s", err, output)
	}
	lts, err := m.ocamlLTS()
	if err != nil {
		return "", fmt.Errorf("query OCaml release policy: %w", err)
	}
	if lts != "" {
		for _, version := range strings.Fields(output) {
			if version == lts {
				return "ocaml-base-compiler." + lts, nil
			}
		}
		return "", fmt.Errorf("advertised OCaml LTS %s is not available in opam", lts)
	}
	return latestCompiler(output)
}

func (m *manager) opamInit(reinit bool) error {
	args := []string{m.at(".local/bin/opam"), "init", "--no-setup", "--enable-shell-hook", "--shell=sh", "--yes"}
	if reinit {
		args = append(args, "--reinit")
	} else {
		args = append(args, "--bare")
	}
	if os.Getenv("SKY_TOOLS_DISABLE_OPAM_SANDBOX") == "1" {
		args = append(args, "--disable-sandboxing")
	}
	return m.run(args...)
}

func (m *manager) ensureOCaml() error {
	if !executable(m.at(".local/bin/opam")) {
		if err := m.installOpam(); err != nil {
			return err
		}
	}
	if _, err := os.Stat(m.at(".opam/config")); os.IsNotExist(err) {
		if err := m.opamInit(false); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	output, err := m.output(m.at(".local/bin/opam"), "switch", "list", "--short")
	if err != nil {
		return fmt.Errorf("list opam switches: %w: %s", err, output)
	}
	for _, name := range strings.Fields(output) {
		if name == "default" {
			if executable(m.at(".opam/default/bin/ocaml")) && executable(m.at(".opam/default/bin/ocamlc")) {
				return nil
			}
			return m.run(m.at(".local/bin/opam"), "reinstall", "--switch=default", "--yes", "ocaml-base-compiler")
		}
	}
	compiler, err := m.compiler()
	if err != nil {
		return err
	}
	return m.run(m.at(".local/bin/opam"), "switch", "create", "default", compiler, "--yes")
}

func (m *manager) updateOCaml() error {
	if err := m.installOpam(); err != nil {
		return err
	}
	if err := m.opamInit(true); err != nil {
		return err
	}
	if err := m.run(m.at(".local/bin/opam"), "update", "--yes"); err != nil {
		return err
	}
	compiler, err := m.compiler()
	if err != nil {
		return err
	}
	if err := m.run(m.at(".local/bin/opam"), "switch", "set-invariant", "--switch=default", compiler, "--yes"); err != nil {
		return err
	}
	return m.run(m.at(".local/bin/opam"), "upgrade", "--switch=default", "--yes")
}

func (m *manager) ensureLean() error {
	if !executable(m.at(".elan/bin/elan")) {
		if err := m.installer("https://elan.lean-lang.org/elan-init.sh", "sh", "-y", "--no-modify-path", "--default-toolchain", "none"); err != nil {
			return err
		}
	}
	output, err := m.output(m.at(".elan/bin/elan"), "toolchain", "list")
	if err != nil {
		return fmt.Errorf("list elan toolchains: %w: %s", err, output)
	}
	toolchain, err := latestLeanToolchain(output)
	if err != nil {
		if err := m.run(m.at(".elan/bin/elan"), "toolchain", "install", "leanprover/lean4:stable"); err != nil {
			return err
		}
		return m.pinLeanDefault()
	}
	// A concrete default avoids network resolution of the stable alias on every
	// proxy invocation. Only update explicitly resolves and downloads stable again.
	return m.run(m.at(".elan/bin/elan"), "default", toolchain)
}

func (m *manager) pinLeanDefault() error {
	output, err := m.output(m.at(".elan/bin/elan"), "toolchain", "list")
	if err != nil {
		return fmt.Errorf("list installed Lean toolchains: %w: %s", err, output)
	}
	toolchain, err := latestLeanToolchain(output)
	if err != nil {
		return fmt.Errorf("Lean installer did not register a stable toolchain: %w", err)
	}
	return m.run(m.at(".elan/bin/elan"), "default", toolchain)
}

func (m *manager) updateLean() error {
	if err := m.run(m.at(".elan/bin/elan"), "self", "update"); err != nil {
		return err
	}
	if err := m.run(m.at(".elan/bin/elan"), "toolchain", "install", "leanprover/lean4:stable"); err != nil {
		return err
	}
	return m.pinLeanDefault()
}

func (m *manager) ensurePi() error {
	if executable(m.at(".local/share/pnpm/bin/pi")) || executable(m.at(".local/share/pnpm/pi")) {
		return nil
	}
	if err := os.MkdirAll(m.at(".local/share/pnpm/bin"), 0755); err != nil {
		return err
	}
	return m.run("pnpm", "add", "--global", "--ignore-scripts", "@earendil-works/pi-coding-agent")
}
