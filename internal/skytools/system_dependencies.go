package skytools

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Capabilities belong to the tool model; platform package names belong to a
// provider. Another operating system can implement this interface independently.
type SystemDependency struct {
	Capability string
	RequiredBy []string
}
type MissingSystemDependency struct {
	Capability           string
	Packages, RequiredBy []string
}
type SystemDependencyReport struct {
	Platform, InstallCommand string
	Missing                  []MissingSystemDependency
	// InstallSteps are the commands that install Missing, to run as root. The
	// dashboard runs them only when the user presses a key on a screen showing them.
	InstallSteps [][]string
}
type SystemPackageProvider interface {
	Check(context.Context, []SystemDependency) (SystemDependencyReport, error)
}

// Installation is an explicit capability, separate from read-only checks.
type SystemPackageInstaller interface {
	Install(context.Context, []SystemDependency) error
}

type systemDependenciesError struct{ report SystemDependencyReport }

func (e *systemDependenciesError) Error() string {
	return "missing system packages: " + strings.Join(e.report.packageNames(), ", ") + "; run: " + e.report.InstallCommand
}
func (r SystemDependencyReport) packageNames() []string {
	set := map[string]bool{}
	for _, dep := range r.Missing {
		for _, name := range dep.Packages {
			set[name] = true
		}
	}
	var names []string
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type toolRequirements struct{ Tools, Install, Runtime []string }

var packageRequirements = map[string]toolRequirements{
	"uv":             {Install: []string{"certificates", "http-client", "tar", "gzip"}},
	"rust":           {Install: []string{"certificates", "http-client", "tar", "gzip", "xz"}, Runtime: []string{"c-build", "pkg-config"}},
	"node":           {Install: []string{"certificates", "http-client", "bash", "git", "tar", "xz"}, Runtime: []string{"cxx-runtime"}},
	"bun":            {Install: []string{"certificates", "http-client", "bash", "unzip"}},
	"dotnet":         {Install: []string{"certificates", "http-client", "bash", "tar", "gzip"}, Runtime: []string{"certificates", "libc-runtime", "gcc-runtime", "icu-runtime", "ssl-runtime", "cxx-runtime", "zlib-runtime", "kerberos-runtime", "timezone-data"}},
	"go":             {Install: []string{"certificates"}},
	"jdk":            {Install: []string{"certificates"}, Runtime: []string{"cxx-runtime", "zlib-runtime"}},
	"fzf":            {Install: []string{"certificates", "git"}},
	"gh":             {Install: []string{"certificates"}},
	"ocaml":          {Install: []string{"certificates", "http-client", "tar", "gzip", "unzip", "git", "c-build", "m4", "pkg-config", "gmp-dev", "process-sandbox", "file-sync"}, Runtime: []string{"c-build"}},
	"lean":           {Install: []string{"certificates", "http-client", "tar", "gzip"}, Runtime: []string{"c-build", "gmp-runtime"}},
	"claude":         {Install: []string{"certificates", "http-client", "bash"}, Runtime: []string{"cxx-runtime"}},
	"codex":          {Install: []string{"certificates", "http-client", "tar", "gzip"}},
	"opencode":       {Install: []string{"certificates", "http-client", "bash", "tar", "gzip"}},
	"pi":             {Tools: []string{"node"}, Install: []string{"certificates"}},
	"btop":           {Install: []string{"certificates"}, Runtime: []string{"terminfo"}},
	"tailscale":      {Install: []string{"certificates"}},
	"playwright":     {Tools: []string{"node"}, Install: []string{"certificates"}, Runtime: []string{"playwright-browsers"}},
	"playwright-cli": {Tools: []string{"playwright"}, Install: []string{"certificates"}, Runtime: []string{"playwright-browsers"}},
}

func dependenciesFor(command string, tools []tool, states []PackageStatus) []SystemDependency {
	byCapability := map[string][]string{}
	for i, t := range tools {
		requirements := packageRequirements[t.name]
		ids := append([]string{}, requirements.Runtime...)
		if command == "update" || i >= len(states) || !states[i].Installed {
			ids = append(ids, requirements.Install...)
		}
		for _, id := range ids {
			found := false
			for _, name := range byCapability[id] {
				found = found || name == t.name
			}
			if !found {
				byCapability[id] = append(byCapability[id], t.name)
			}
		}
	}
	var result []SystemDependency
	for id, names := range byCapability {
		result = append(result, SystemDependency{id, names})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Capability < result[j].Capability })
	return result
}

// dependenciesForPlan merges each step's requirements, because one plan can mix
// installing some tools with updating others.
func dependenciesForPlan(steps []planStep, states []PackageStatus) []SystemDependency {
	byCapability := map[string][]string{}
	for i, step := range steps {
		for _, dep := range dependenciesFor(step.command, []tool{step.tool}, states[i:i+1]) {
			byCapability[dep.Capability] = append(byCapability[dep.Capability], dep.RequiredBy...)
		}
	}
	var result []SystemDependency
	for id, names := range byCapability {
		result = append(result, SystemDependency{id, names})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Capability < result[j].Capability })
	return result
}

func (m *manager) checkSystemDependencies(command string, tools []tool) (SystemDependencyReport, error) {
	steps := make([]planStep, len(tools))
	for i, t := range tools {
		steps[i] = planStep{t, command}
	}
	return m.checkPlanDependencies(steps)
}

func (m *manager) checkPlanDependencies(steps []planStep) (SystemDependencyReport, error) {
	tools := make([]tool, len(steps))
	for i, step := range steps {
		tools[i] = step.tool
	}
	deps := configuredSystemDependencies(dependenciesForPlan(steps, m.scan(tools)))
	if len(deps) == 0 {
		return SystemDependencyReport{}, nil
	}
	provider, err := m.systemPackageProvider()
	if err != nil {
		return SystemDependencyReport{}, err
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	return provider.Check(ctx, deps)
}

func configuredSystemDependencies(deps []SystemDependency) []SystemDependency {
	if os.Getenv("SKY_TOOLS_DISABLE_OPAM_SANDBOX") == "1" {
		filtered := deps[:0]
		for _, dep := range deps {
			if dep.Capability != "process-sandbox" {
				filtered = append(filtered, dep)
			}
		}
		deps = filtered
	}
	return deps
}

func (m *manager) systemPackageProvider() (SystemPackageProvider, error) {
	provider := m.systemProvider
	if provider == nil {
		id, like := nativeValue("/etc/os-release", "ID"), nativeValue("/etc/os-release", "ID_LIKE")
		if runtime.GOOS != "linux" || !(id == "debian" || id == "ubuntu" || containsWord(like, "debian") || containsWord(like, "ubuntu")) {
			return nil, fmt.Errorf("system dependencies currently support Debian-based Linux only (detected %s %s)", runtime.GOOS, id)
		}
		provider = debianPackageProvider{
			architecture: runtime.GOARCH,
			distribution: id,
			version:      nativeValue("/etc/os-release", "VERSION_ID"),
			query: func(ctx context.Context) (string, error) {
				return m.outputContext(ctx, "/usr/bin/dpkg-query", "--show", "--showformat=${Package}\t${Architecture}\t${db:Status-Status}\t${Provides}\n")
			},
			run: func(ctx context.Context, args ...string) error {
				cmd, err := m.command(ctx, args...)
				if err != nil {
					return err
				}
				cmd.Env = setEnv(cmd.Env, "DEBIAN_FRONTEND", "noninteractive")
				cmd.Stdout, cmd.Stderr = m.out, m.errOut
				return cmd.Run()
			},
		}
	}
	return provider, nil
}

func (m *manager) installSystemDependencies(tools []tool) error {
	provider, err := m.systemPackageProvider()
	if err != nil {
		return err
	}
	installer, ok := provider.(SystemPackageInstaller)
	if !ok {
		return fmt.Errorf("system package provider does not support installation")
	}
	deps := configuredSystemDependencies(dependenciesFor("update", tools, nil))
	m.log("installing system prerequisites for %s", toolNames(tools))
	return installer.Install(m.ctx, deps)
}

func containsWord(text, word string) bool {
	for _, value := range strings.Fields(text) {
		if value == word {
			return true
		}
	}
	return false
}

func (m *manager) preflight(steps []planStep) error {
	report, err := m.checkPlanDependencies(steps)
	if err != nil {
		return fmt.Errorf("check system dependencies: %w", err)
	}
	if len(report.Missing) == 0 {
		if report.Platform != "" {
			m.log("prerequisites ok (%s)", report.Platform)
		}
		return nil
	}
	selected := make([]tool, len(steps))
	for i, step := range steps {
		selected[i] = step.tool
	}
	m.log("missing system packages (%s):", report.Platform)
	for _, dep := range report.Missing {
		m.log("  %s: %s (needed by %s)", dep.Capability, strings.Join(dep.Packages, ", "), strings.Join(dep.RequiredBy, ", "))
	}
	m.log("run: %s", report.InstallCommand)
	m.log("or run as root: skytools deps install --only %s", toolNames(selected))
	m.log("inspect requirements with: skytools deps --only %s", toolNames(selected))
	return &systemDependenciesError{report}
}

func toolNames(tools []tool) string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.name
	}
	return strings.Join(names, ",")
}
