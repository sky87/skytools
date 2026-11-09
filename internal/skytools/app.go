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

const usage = `skytools sets up and updates developer tools in your home directory.

usage: skytools [command] [--only tool,tool]

Interactive
  skytools             Open the dashboard. Prints the inventory when piped.
  ui                   Same as no command

Install and update
  ensure               Install missing tools; keep what is installed
  update               Update installed tools to the latest LTS or stable;
                       never installs tools that are missing
  self-update          Update skytools itself from the latest GitHub release

Inspect
  status               Local inventory; reads files only
  status --versions    Also ask each tool for its version (may be slow)
  list                 Names accepted by --only
  version              Show the skytools version

System and shell
  deps                 Check the OS packages tools need; changes nothing
  deps install         Install them (root on Debian)
  setup                Add the shell hook without installing tools
  env                  Print shell environment: eval "$(skytools env)"

--only takes a comma-separated list for ensure, update, status and deps, and
includes dependencies. update skips listed tools that are not installed.
Tools install under $HOME. Only deps install changes system packages.
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
		fmt.Fprintf(errOut, "skytools: unknown command %q; run skytools --help\n", command)
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
	if command == "update" {
		var skipped []string
		only, skipped, err = m.installedForUpdate(tools, only)
		if err != nil {
			fmt.Fprintln(errOut, "skytools:", err)
			return 2
		}
		if len(skipped) > 0 {
			m.log("not installed, so not updated: %s (install with: skytools ensure --only %s)", strings.Join(skipped, ", "), strings.Join(skipped, ","))
		}
		if only == "" {
			m.log("nothing to update: none of the selected tools is installed")
			return 0
		}
	}
	selected, err := selectTools(tools, only, command != "status")
	if err != nil {
		fmt.Fprintln(errOut, "skytools:", err)
		return 2
	}
	if command == "list" {
		for _, tool := range tools {
			fmt.Fprintf(out, "%-15s %s\n", tool.name, tool.label)
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
			printInventory(out, m.home, m.scan(selected))
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

// installedForUpdate narrows update to tools already installed, completely or
// partly (update repairs those): without --only that is every installed tool,
// and listed tools that are missing are returned as skipped. Unknown names are
// still errors. The result is an --only list; its dependencies are added later
// as for ensure, so an installed tool keeps what it needs to run.
func (m *manager) installedForUpdate(all []tool, only string) (string, []string, error) {
	listed, err := selectTools(all, only, false)
	if err != nil {
		return "", nil, err
	}
	named := only != ""
	for _, name := range strings.Split(only, ",") {
		if strings.TrimSpace(name) == "all" {
			named = false
		}
	}
	var installed, skipped []string
	for _, status := range m.scan(listed) {
		switch {
		case status.Installed || status.Partial:
			installed = append(installed, status.Name)
		case named:
			skipped = append(skipped, status.Name)
		}
	}
	return strings.Join(installed, ","), skipped, nil
}

// perform is shared by the command line and the tests: every tool gets the same
// command. Callers expand dependencies with selectTools before entering it.
func (m *manager) perform(command string, selected []tool, report func(stepEvent)) error {
	var steps []planStep
	if command != "setup" {
		for _, t := range selected {
			steps = append(steps, planStep{t, command})
		}
	}
	return m.performPlan(steps, report)
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
					output = "missing"
				}
			}
			fmt.Fprintf(m.out, "%-15s %s\n", v.label, strings.Split(strings.TrimSpace(output), "\n")[0])
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
