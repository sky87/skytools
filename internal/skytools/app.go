package skytools

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const usage = `usage: skytools <command> [--only tool,tool]

Commands:
  ensure   Install missing tools and configure your shell; keep existing versions
  update   Ensure tools exist, then update them
  status   Show versions of managed tools (does not install anything)
  list     List tool names accepted by --only
  env      Print shell environment; use: eval "$(skytools env)"
  setup    Configure shell startup files without installing tools
  version  Show the skytools version

Options for ensure, update, status:
  --only   Comma-separated tool names; default: all (pi also requires node)

Installations live under $HOME. No sudo or system package changes.
`

func Run(ctx context.Context, args []string, out, errOut io.Writer, version string) int {
	if len(args) == 0 {
		fmt.Fprint(errOut, usage)
		return 2
	}
	command := args[0]
	if command == "--help" || command == "-h" || command == "help" {
		fmt.Fprint(out, usage)
		return 0
	}
	if command == "version" || command == "--version" {
		if len(args) != 1 {
			return 2
		}
		fmt.Fprintln(out, "skytools "+version)
		return 0
	}
	switch command {
	case "ensure", "update", "status", "list", "env", "setup":
	default:
		fmt.Fprint(errOut, usage)
		return 2
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	only := ""
	if command == "ensure" || command == "update" || command == "status" {
		flags.StringVar(&only, "only", "", "comma-separated tool names")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "skytools: unexpected arguments")
		return 2
	}
	if command == "env" {
		fmt.Fprint(out, shellEnv)
		return 0
	}
	m, err := newManager(ctx, out, errOut)
	if err != nil {
		fmt.Fprintln(errOut, "skytools:", err)
		return 1
	}
	tools := m.tools()
	selected, err := selectTools(tools, only, command != "status")
	if err != nil {
		fmt.Fprintln(errOut, "skytools:", err)
		return 2
	}
	if command == "list" {
		for _, tool := range tools {
			fmt.Fprintf(out, "%-12s %s\n", tool.name, tool.label)
		}
		return 0
	}
	if command == "status" {
		m.status(selected)
		return 0
	}
	if err := m.platform(); err != nil {
		fmt.Fprintln(errOut, "skytools:", err)
		return 1
	}
	err = m.withLock(func() error {
		if command != "setup" {
			for _, tool := range selected {
				start := time.Now()
				m.log("checking %s", tool.label)
				if err := tool.ensure(); err != nil {
					return fmt.Errorf("%s: %w", tool.name, err)
				}
				if command == "update" && tool.update != nil {
					m.log("updating %s", tool.label)
					if err := tool.update(); err != nil {
						return fmt.Errorf("%s: %w", tool.name, err)
					}
				}
				m.log("%s is available (%s)", tool.label, time.Since(start).Round(time.Second))
			}
		}
		if err := m.setupShell(); err != nil {
			return err
		}
		m.log("shell configured; open a new shell or run: eval \"$(skytools env)\"")
		return nil
	})
	if err != nil {
		fmt.Fprintln(errOut, "skytools:", err)
		return 1
	}
	return 0
}

type tool struct {
	name, label    string
	ensure, update func() error
	versions       []versionCommand
}

type versionCommand struct {
	label string
	args  []string
}

func selectTools(all []tool, only string, dependencies bool) ([]tool, error) {
	if only == "" {
		return all, nil
	}
	wanted := map[string]bool{}
	for _, name := range strings.Split(only, ",") {
		name = strings.TrimSpace(name)
		found := false
		for _, t := range all {
			if t.name == name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown tool %q; run skytools list", name)
		}
		wanted[name] = true
	}
	if dependencies && wanted["pi"] {
		wanted["node"] = true
	}
	var result []tool
	for _, t := range all {
		if wanted[t.name] {
			result = append(result, t)
		}
	}
	return result, nil
}

func (m *manager) platform() error {
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin") ||
		(runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return fmt.Errorf("unsupported platform %s/%s; supported: Linux (glibc) and macOS, amd64/arm64", runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

func (m *manager) status(tools []tool) {
	for _, t := range tools {
		for _, v := range t.versions {
			ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
			output, err := m.outputContext(ctx, v.args...)
			cancel()
			if err == nil && v.label == "lean" {
				// The Lean proxy can download a tracking toolchain as a side effect.
				// Report the newest locally registered stable version. Current elan
				// lists names without the default markers used by older releases.
				output, err = latestLeanToolchain(output)
			}
			if err != nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					output = "version check timed out"
				} else {
					output = "not installed / unavailable"
				}
			}
			fmt.Fprintf(m.out, "%-12s %s\n", v.label, strings.Split(strings.TrimSpace(output), "\n")[0])
		}
	}
}

func (m *manager) at(parts ...string) string {
	return filepath.Join(append([]string{m.home}, parts...)...)
}

func executable(name string) bool {
	info, err := os.Stat(name)
	return err == nil && !info.IsDir() && info.Mode()&0111 != 0
}
