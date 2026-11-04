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

const usage = `usage: skytools [command] [--only tool,tool]

Without a command, open the interactive package dashboard on a terminal,
or print a fast, read-only package inventory when output is redirected.

Commands:
  ensure       Install missing tools and configure your shell; keep existing versions
  update       Ensure tools exist, then update them
  status       Show a fast, read-only local package inventory
  deps         Check system prerequisites and show missing Debian packages
  deps install Install selected tools' system prerequisites (root on Debian)
  ui           Open the interactive package dashboard
  list         List tool names accepted by --only
  env          Print shell environment; use: eval "$(skytools env)"
  setup        Configure shell startup files without installing tools
  self-update  Update skytools itself from the latest stable GitHub release
  version      Show the skytools version

Options for ensure, update, status, deps:
  --only   Comma-separated tool names; default: all (dependencies included)

Options for status:
  --versions  Run managed tools to query their versions (may be slow)

Tools install under $HOME. Only explicit deps install changes system packages.
`

func Run(ctx context.Context, args []string, out, errOut io.Writer, version string) int {
	if len(args) == 0 {
		args = []string{"ui"}
	}
	command := args[0]
	installDeps := command == "deps" && len(args) > 1 && args[1] == "install"
	if installDeps {
		args = append([]string{"deps"}, args[2:]...)
	}
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
	case "ensure", "update", "status", "list", "env", "setup", "ui", "deps", "self-update":
	default:
		fmt.Fprint(errOut, usage)
		return 2
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errOut)
	only := ""
	versions := false
	if command == "ensure" || command == "update" || command == "status" || command == "deps" {
		flags.StringVar(&only, "only", "", "comma-separated tool names")
	}
	if command == "status" {
		flags.BoolVar(&versions, "versions", false, "run managed tools to query versions")
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
	if command == "self-update" {
		path, err := os.Executable()
		if err == nil {
			err = m.selfUpdate(version, path)
		}
		if err != nil {
			fmt.Fprintln(errOut, "skytools:", err)
			return 1
		}
		return 0
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
	if command == "deps" {
		if installDeps {
			if err := m.installSystemDependencies(selected); err != nil {
				fmt.Fprintln(errOut, "skytools:", err)
				return 1
			}
			fmt.Fprintln(out, "All required system packages are installed.")
			return 0
		}
		report, err := m.checkSystemDependencies("update", selected)
		if err != nil {
			fmt.Fprintln(errOut, "skytools:", err)
			return 1
		}
		if len(report.Missing) == 0 {
			fmt.Fprintln(out, "All required system packages are installed.")
			return 0
		}
		for _, dep := range report.Missing {
			fmt.Fprintf(out, "%s: %s (needed by %s)\n", dep.Capability, strings.Join(dep.Packages, ", "), strings.Join(dep.RequiredBy, ", "))
		}
		fmt.Fprintln(out, report.InstallCommand)
		return 1
	}
	if command == "status" {
		if versions {
			m.status(selected)
		} else {
			printInventory(out, m.scan(selected))
		}
		return 0
	}
	if command == "ui" {
		if err := m.dashboard(version, os.Stdin, out); err != nil {
			fmt.Fprintln(errOut, "skytools:", err)
			return 1
		}
		return 0
	}
	if err := m.perform(command, selected, nil); err != nil {
		fmt.Fprintln(errOut, "skytools:", err)
		return 1
	}
	return 0
}

// perform is shared by command-line and dashboard operations. Callers expand
// dependencies with selectTools before entering this function.
func (m *manager) perform(command string, selected []tool, started func(string)) error {
	if err := m.platform(); err != nil {
		return err
	}
	return m.withLock(func() error {
		if err := m.ctx.Err(); err != nil {
			return err
		}
		if command != "setup" {
			if err := m.preflight(command, selected); err != nil {
				return err
			}
			for _, packageTool := range selected {
				if err := m.ctx.Err(); err != nil {
					return err
				}
				if started != nil {
					started(packageTool.label)
				}
				start := time.Now()
				m.log("checking %s", packageTool.label)
				before := m.scan([]tool{packageTool})
				if command == "update" || !before[0].Installed {
					if err := packageTool.ensure(); err != nil {
						return fmt.Errorf("%s: %w", packageTool.name, err)
					}
				}
				if command == "update" && packageTool.update != nil {
					m.log("updating %s", packageTool.label)
					if err := packageTool.update(); err != nil {
						return fmt.Errorf("%s: %w", packageTool.name, err)
					}
				}
				if err := m.ctx.Err(); err != nil {
					return err
				}
				after := m.scan([]tool{packageTool})[0]
				if !after.Installed || (command == "update" || !before[0].Installed) && after.Origin != "managed" {
					return fmt.Errorf("%s: installation is still missing or incomplete", packageTool.name)
				}
				m.log("%s is available (%s)", packageTool.label, time.Since(start).Round(time.Second))
			}
		}
		if err := m.ctx.Err(); err != nil {
			return err
		}
		if started != nil {
			started("shell configuration")
		}
		if err := m.setupShell(); err != nil {
			return err
		}
		m.log("shell configured; open a new shell or run: eval \"$(skytools env)\"")
		return nil
	})
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
	return selectToolsWithRequirements(all, only, dependencies, packageRequirements)
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
