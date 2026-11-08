package skytools

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Shell completion. completion.bash and completion.zsh complete skytools itself
// and load the managed tools' own completion scripts, which writeCompletions
// generates from each managed executable at the end of a run. Tools without a
// completion generator (Bun, lazygit, OpenCode, Claude Code, pi, ...) have none.

//go:embed completion.bash
var bashCompletion string

//go:embed completion.zsh
var zshCompletion string

const completionDir = ".local/share/skytools/completions"

type completionSource struct {
	command string
	// binary is the home-relative managed executable; system copies are left
	// to their own packages' completion.
	binary string
	// args run binary to print the script; "{shell}" becomes bash or zsh.
	args []string
	// env is an extra environment entry for the generator.
	env string
	// sysroot, instead of args, copies the scripts a Rust toolchain ships,
	// relative to `binary --print sysroot`.
	sysroot map[string]string
	// zshSourced marks zsh output that registers itself with compdef when
	// sourced, rather than a #compdef function file.
	zshSourced bool
	// aliases are ~/.local/bin links to binary that complete the same way.
	aliases []string
}

func completionSources() []completionSource {
	return []completionSource{
		{command: "rg", binary: ".local/bin/rg", args: []string{"--generate", "complete-{shell}"}},
		{command: "fd", binary: ".local/bin/fd", args: []string{"--gen-completions", "{shell}"}},
		{command: "ast-grep", binary: ".local/bin/ast-grep", args: []string{"completions", "{shell}"}, aliases: []string{"sg"}},
		{command: "delta", binary: ".local/bin/delta", args: []string{"--generate-completion", "{shell}"}},
		{command: "just", binary: ".local/bin/just", env: "JUST_COMPLETE={shell}", zshSourced: true},
		{command: "gh", binary: ".local/bin/gh", args: []string{"completion", "-s", "{shell}"}},
		{command: "tailscale", binary: ".local/bin/tailscale", args: []string{"completion", "{shell}"}},
		{command: "uv", binary: ".local/bin/uv", args: []string{"generate-shell-completion", "{shell}"}},
		{command: "uvx", binary: ".local/bin/uvx", args: []string{"--generate-shell-completion", "{shell}"}},
		{command: "codex", binary: ".local/bin/codex", args: []string{"completion", "{shell}"}},
		{command: "rustup", binary: ".cargo/bin/rustup", args: []string{"completions", "{shell}", "rustup"}},
		{command: "cargo", binary: ".cargo/bin/rustc", sysroot: map[string]string{
			"bash": "etc/bash_completion.d/cargo", "zsh": "share/zsh/site-functions/_cargo"}},
		{command: "elan", binary: ".elan/bin/elan", args: []string{"completions", "{shell}"}},
		{command: "dotnet", binary: ".dotnet/dotnet", args: []string{"completions", "script", "{shell}"}},
		{command: "npm", binary: ".local/bin/npm", args: []string{"completion"}, zshSourced: true},
		{command: "pnpm", binary: ".local/bin/pnpm", args: []string{"completion", "{shell}"}, zshSourced: true},
	}
}

// completionFile is where a command's script for shell lives, relative to
// completionDir.
func completionFile(command, shell string, zshSourced bool) string {
	switch {
	case shell == "bash":
		return filepath.Join("bash", command)
	case zshSourced:
		return filepath.Join("zsh", command+".zsh")
	default:
		return filepath.Join("zsh", "_"+command)
	}
}

const completionStampPrefix = "# skytools completion source: "

// writeCompletions brings the generated scripts in line with the managed
// executables: it writes missing ones, regenerates those whose executable
// changed, and removes everything else in the directory. A tool whose
// generator fails is reported and gets no completion; the run still succeeds.
func (m *manager) writeCompletions() error {
	root := m.at(completionDir)
	for _, shell := range []string{"bash", "zsh"} {
		if err := os.MkdirAll(filepath.Join(root, shell), 0755); err != nil {
			return err
		}
	}
	wanted := map[string]bool{}
	for _, source := range completionSources() {
		binary := m.at(source.binary)
		if !executable(binary) {
			continue
		}
		var aliases []string
		for _, alias := range source.aliases {
			if target, err := filepath.EvalSymlinks(m.at(".local/bin", alias)); err == nil {
				if resolved, err := filepath.EvalSymlinks(binary); err == nil && target == resolved {
					aliases = append(aliases, alias)
				}
			}
		}
		for _, shell := range []string{"bash", "zsh"} {
			name := completionFile(source.command, shell, source.zshSourced)
			path := filepath.Join(root, name)
			stamp, err := m.completionStamp(source, binary, shell)
			if err == nil && completionCurrent(path, stamp) {
				wanted[name] = true
			} else if err == nil {
				var script []byte
				if script, err = m.completionScript(source, binary, shell); err == nil {
					script = completionAliases(script, source.command, aliases, shell)
					script = append(bytes.TrimRight(script, "\n"), "\n"+completionStampPrefix+stamp+"\n"...)
					err = atomicWrite(path, script, 0644)
				}
				if err == nil {
					wanted[name] = true
				}
			}
			if err != nil {
				m.log("no %s completion for %s: %v", shell, source.command, err)
				continue
			}
			if shell == "bash" {
				for _, alias := range aliases {
					// Sourcing the command's script registers the alias as well.
					aliasName := completionFile(alias, "bash", false)
					data := []byte(". \"${BASH_SOURCE[0]%/*}/" + source.command + "\"\n")
					if err := atomicWrite(filepath.Join(root, aliasName), data, 0644); err != nil {
						return err
					}
					wanted[aliasName] = true
				}
			}
		}
	}
	for _, shell := range []string{"bash", "zsh"} {
		entries, err := os.ReadDir(filepath.Join(root, shell))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if name := filepath.Join(shell, entry.Name()); !wanted[name] {
				if err := os.Remove(filepath.Join(root, name)); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	}
	return nil
}

// completionStamp identifies the file a script is generated from, so a changed
// executable or toolchain regenerates it. Installers replace executables rather
// than rewriting them, and versioned installs change the resolved path.
func (m *manager) completionStamp(source completionSource, binary, shell string) (string, error) {
	origin := binary
	if source.sysroot != nil {
		sysroot, err := m.completionOutput(binary, "", "--print", "sysroot")
		if err != nil {
			return "", err
		}
		origin = filepath.Join(strings.TrimSpace(string(sysroot)), source.sysroot[shell])
	}
	resolved, err := filepath.EvalSymlinks(origin)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %d %d", resolved, info.Size(), info.ModTime().UnixNano()), nil
}

func completionCurrent(path, stamp string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.HasSuffix(data, []byte("\n"+completionStampPrefix+stamp+"\n"))
}

func (m *manager) completionScript(source completionSource, binary, shell string) ([]byte, error) {
	if source.sysroot != nil {
		stamp, err := m.completionStamp(source, binary, shell)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(strings.SplitN(stamp, " ", 2)[0])
	}
	args := make([]string, len(source.args))
	for i, arg := range source.args {
		args[i] = strings.ReplaceAll(arg, "{shell}", shell)
	}
	script, err := m.completionOutput(binary, strings.ReplaceAll(source.env, "{shell}", shell), args...)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(script)) == 0 {
		return nil, fmt.Errorf("%s printed no script", source.command)
	}
	return script, nil
}

// completionOutput runs a generator with a time limit and returns only its
// standard output; diagnostics on standard error must not end up in a script.
func (m *manager) completionOutput(binary, env string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	cmd, err := m.command(ctx, append([]string{binary}, args...)...)
	if err != nil {
		return nil, err
	}
	if env != "" {
		cmd.Env = append(cmd.Env, env)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%w: %s", err, firstLine(message))
		}
		return nil, err
	}
	return output, nil
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

// completionAliases makes a script complete aliases too: bash `complete` lines
// for the command and the zsh #compdef line name them as well.
func completionAliases(script []byte, command string, aliases []string, shell string) []byte {
	if len(aliases) == 0 {
		return script
	}
	extra := " " + strings.Join(aliases, " ")
	lines := strings.Split(string(script), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case shell == "zsh" && i == 0 && trimmed == "#compdef "+command:
			lines[i] = line + extra
		case shell == "bash" && strings.HasPrefix(trimmed, "complete ") && strings.HasSuffix(trimmed, " "+command):
			lines[i] = line + extra
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
