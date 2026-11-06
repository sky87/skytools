package skytools

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PackageStatus is a read-only snapshot. Version is empty when upstream does not
// leave cheap native metadata. Channel describes the installation policy, not
// a claim that the local version is the newest upstream release.
type PackageStatus struct {
	Name, Label, Path, Version, Detail, Channel, Origin string
	Installed, Partial                                  bool
}

// scan performs filesystem inspection only: no commands, downloads, or writes.
// A partial managed installation takes precedence over a system installation so
// the user can see and repair an interrupted installation in their home.
func (m *manager) scan(tools []tool) []PackageStatus {
	result := make([]PackageStatus, 0, len(tools))
	for _, t := range tools {
		s := m.managedStatus(t.name)
		if !s.Installed && !s.Partial {
			s = m.systemStatus(t.name)
		}
		s.Name, s.Label, s.Channel = t.name, t.label, packageChannel(t.name)
		if s.Installed {
			detail := s.Detail
			if s.Origin == "system" {
				s.Detail = "Available on PATH; updates install a managed copy in your home"
			} else {
				s.Detail = "Installed in your home"
			}
			if detail != "" {
				s.Detail += " | " + detail
			}
		} else if s.Partial {
			if s.Detail == "" {
				s.Detail = "Incomplete installation; repair required"
			}
		} else {
			s.Detail = "Not installed"
		}
		result = append(result, s)
	}
	return result
}

func packageChannel(name string) string {
	switch name {
	case "node", "dotnet", "jdk":
		return "LTS"
	default:
		return "stable"
	}
}

func exists(name string) bool { _, err := os.Stat(name); return err == nil }
func regularFile(name string) bool {
	info, err := os.Stat(name)
	return err == nil && info.Mode().IsRegular()
}

// Native metadata is small. Bound reads so a corrupt file cannot stall startup
// by making the scanner read an arbitrary executable or a huge metadata file.
func nativeText(name string) string {
	info, err := os.Stat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return ""
	}
	f, err := os.Open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func nativeValue(name, key string) string {
	for _, line := range strings.Split(nativeText(name), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			return strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		}
	}
	return ""
}

func resolved(name string) string {
	path, err := filepath.EvalSymlinks(name)
	if err != nil {
		return ""
	}
	return path
}

func distributionRoot(binary string) string {
	if binary = resolved(binary); binary != "" {
		return filepath.Dir(filepath.Dir(binary))
	}
	return ""
}

func jdkVersion(root string) string {
	if version := nativeValue(filepath.Join(root, "release"), "IMPLEMENTOR_VERSION"); strings.HasPrefix(version, "Corretto-") {
		return strings.TrimPrefix(version, "Corretto-")
	}
	return nativeValue(filepath.Join(root, "release"), "JAVA_VERSION")
}
func correttoJDK(root string) bool {
	vendor := strings.ToLower(nativeValue(filepath.Join(root, "release"), "IMPLEMENTOR") + " " + nativeValue(filepath.Join(root, "release"), "IMPLEMENTOR_VERSION"))
	return executable(filepath.Join(root, "bin/java")) && executable(filepath.Join(root, "bin/javac")) && jdkVersion(root) != "" && (strings.Contains(vendor, "amazon") || strings.Contains(vendor, "corretto"))
}

func sdkVersion(root string) string {
	entries, _ := os.ReadDir(filepath.Join(root, "sdk"))
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() && releaseVersion.MatchString(entry.Name()) && exists(filepath.Join(root, "sdk", entry.Name(), "dotnet.dll")) {
			versions = append(versions, entry.Name())
		}
	}
	version, _ := latestStableVersion(strings.Join(versions, " "))
	return version
}

func nativePackageVersion(binary string) string {
	path := resolved(binary)
	for dir, i := filepath.Dir(path), 0; path != "" && i < 5 && dir != filepath.Dir(dir); dir, i = filepath.Dir(dir), i+1 {
		var p struct {
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(nativeText(filepath.Join(dir, "package.json"))), &p) == nil && releaseVersion.MatchString(p.Version) {
			return p.Version
		}
	}
	return ""
}

func (m *manager) managedStatus(name string) PackageStatus {
	if name == "playwright" || name == "playwright-cli" {
		return m.managedPlaywrightStatus(name)
	}
	s := PackageStatus{Origin: "managed"}
	var paths []string
	switch name {
	case "uv":
		paths = []string{".local/bin/uv"}
	case "bun":
		paths = []string{".bun/bin/bun"}
	case "fzf":
		paths = []string{".fzf/bin/fzf"}
	case "gh":
		paths = []string{".local/bin/gh"}
	case "btop":
		paths = []string{".local/bin/btop"}
	case "tailscale":
		paths = []string{".local/bin/tailscale", ".local/bin/tailscaled"}
	case "claude":
		paths = []string{".local/bin/claude"}
	case "codex":
		paths = []string{".local/bin/codex"}
	case "opencode":
		paths = []string{".opencode/bin/opencode"}
	case "pi":
		for _, path := range []string{".local/share/pnpm/bin/pi", ".local/share/pnpm/pi"} {
			if executable(m.at(path)) {
				paths = []string{path}
				break
			}
		}
		if len(paths) == 0 {
			paths = []string{".local/share/pnpm/bin/pi", ".local/share/pnpm/pi"}
		}
	case "go":
		paths = []string{".go/current/bin/go"}
		s.Version = nativeGoVersion(m.at(".go/current"))
	case "jdk":
		paths = []string{".jdk/current/bin/java", ".jdk/current/bin/javac"}
		s.Version = jdkVersion(m.at(".jdk/current"))
		s.Installed = correttoJDK(m.at(".jdk/current"))
	case "dotnet":
		paths = []string{".dotnet/dotnet"}
		s.Version = sdkVersion(m.at(".dotnet"))
	case "node":
		paths = []string{".local/bin/node", ".local/bin/npm", ".local/bin/pnpm", ".nvm/current/bin/node", ".nvm/current/bin/npm", ".nvm/current/bin/pnpm"}
		root := distributionRoot(m.at(".local/bin/node"))
		if version := strings.TrimPrefix(filepath.Base(root), "v"); releaseVersion.MatchString(version) {
			s.Version = version
		}
	case "ocaml":
		paths = []string{".local/bin/opam", ".opam/default/bin/ocaml", ".opam/default/bin/ocamlc"}
		s.Version = nativeValue(m.at(".opam/default/.opam-switch/switch-config"), "ocaml-version")
	case "rust":
		paths = []string{".cargo/bin/rustup", ".cargo/bin/rustc", ".cargo/bin/cargo"}
		root := m.rustToolchain()
		s.Installed = root != ""
		if root != "" {
			s.Version = rustVersion(root)
		}
	case "lean":
		paths = []string{".elan/bin/elan", ".elan/bin/lean"}
		root, version := m.leanToolchain()
		s.Installed, s.Version = root != "", version
	default:
		return PackageStatus{}
	}
	complete, any := true, false
	for _, path := range paths {
		path = m.at(path)
		if s.Path == "" {
			s.Path = path
		}
		ok := executable(path)
		complete = complete && ok
		// Lstat also exposes a broken install symlink as partial, never installed.
		_, err := os.Lstat(path)
		any = any || err == nil
	}
	switch name {
	case "rust", "lean", "jdk":
		complete = complete && s.Installed
	case "ocaml":
		complete = complete && exists(m.at(".opam/config"))
	case "node":
		complete = complete && regularFile(m.at(".nvm/nvm.sh"))
		any = any || exists(m.at(".nvm/nvm.sh")) || exists(m.at(".nvm/current"))
	case "dotnet":
		complete = complete && s.Version != ""
	case "go":
		complete = complete && goVersion.MatchString(s.Version)
	}
	footprint := map[string]string{"rust": ".rustup", "node": ".nvm", "bun": ".bun", "dotnet": ".dotnet", "go": ".go", "jdk": ".jdk", "fzf": ".fzf", "ocaml": ".opam", "lean": ".elan", "opencode": ".opencode"}[name]
	if footprint != "" {
		_, err := os.Lstat(m.at(footprint))
		any = any || err == nil
	}
	s.Installed, s.Partial = complete, any && !complete
	if !any {
		s.Path, s.Origin = "", ""
	}
	if name == "pi" && complete {
		s.Version = nativePackageVersion(s.Path)
	}
	if name == "claude" && complete {
		if version := filepath.Base(resolved(s.Path)); releaseVersion.MatchString(version) {
			s.Version = version
		}
	}
	return s
}

func rustVersion(root string) string {
	// rustup's own manifest is upstream metadata, not skytools bookkeeping.
	data := nativeText(filepath.Join(root, "lib/rustlib/multirust-channel-manifest.toml"))
	section := false
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "[") {
			section = line == "[pkg.rust]"
		}
		if section && strings.HasPrefix(line, "version = ") {
			fields := strings.Fields(strings.Trim(strings.TrimPrefix(line, "version = "), "\""))
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

func nativeGoVersion(root string) string {
	return strings.Split(nativeText(filepath.Join(root, "VERSION")), "\n")[0]
}

func (m *manager) rustToolchain() string {
	selected := nativeValue(m.at(".rustup/settings.toml"), "default_toolchain")
	if selected == "" {
		return ""
	}
	path := m.at(".rustup/toolchains", selected)
	if executable(filepath.Join(path, "bin/rustc")) && executable(filepath.Join(path, "bin/cargo")) {
		return path
	}
	return ""
}

func (m *manager) installedRustToolchain() string {
	entries, _ := os.ReadDir(m.at(".rustup/toolchains"))
	fallback := ""
	for _, entry := range entries {
		path := m.at(".rustup/toolchains", entry.Name())
		if !executable(filepath.Join(path, "bin/rustc")) || !executable(filepath.Join(path, "bin/cargo")) {
			continue
		}
		if strings.HasPrefix(entry.Name(), "stable-") {
			return entry.Name()
		}
		if releaseVersion.MatchString(strings.SplitN(entry.Name(), "-", 2)[0]) || releaseVersion.MatchString(rustVersion(path)) {
			fallback = entry.Name()
		}
	}
	return fallback
}

func (m *manager) leanToolchain() (string, string) {
	root := m.at(".elan/toolchains")
	selected := nativeValue(m.at(".elan/settings.toml"), "default_toolchain")
	switch selected {
	case "":
		return "", ""
	case "stable", "lean4:stable", "leanprover/lean4:stable":
		// elan's own installer writes this channel alias. elan resolves it to a
		// concrete leanprover--lean4---vX.Y.Z directory on first use, so the
		// default is the newest stable release in that form.
		entries, _ := os.ReadDir(root)
		var list []string
		for _, entry := range entries {
			version := strings.TrimPrefix(entry.Name(), "leanprover--lean4---v")
			if version != entry.Name() && releaseVersion.MatchString(version) && executable(filepath.Join(root, entry.Name(), "bin/lean")) {
				list = append(list, version)
			}
		}
		newest, err := latestStableVersion(strings.Join(list, " "))
		if err != nil {
			return "", ""
		}
		return filepath.Join(root, "leanprover--lean4---v"+newest), newest
	}
	path := filepath.Join(root, strings.NewReplacer("/", "--", ":", "---").Replace(selected))
	if !executable(filepath.Join(path, "bin/lean")) {
		return "", ""
	}
	return path, strings.TrimPrefix(strings.TrimPrefix(selected, "leanprover/lean4:"), "v")
}

func (m *manager) systemStatus(name string) PackageStatus {
	commands := map[string][]string{
		"uv": {"uv"}, "rust": {"rustc", "cargo"}, "node": {"node", "npm", "pnpm"},
		"bun": {"bun"}, "dotnet": {"dotnet"}, "go": {"go"}, "jdk": {"java", "javac"},
		"fzf": {"fzf"}, "gh": {"gh"}, "btop": {"btop"}, "tailscale": {"tailscale"}, "ocaml": {"opam", "ocaml", "ocamlc"},
		"lean": {"lean"}, "claude": {"claude"}, "codex": {"codex"}, "opencode": {"opencode"}, "pi": {"pi"},
	}[name]
	s := PackageStatus{Origin: "system"}
	complete, any := len(commands) > 0, false
	for _, command := range commands {
		path, err := m.lookup(command) // lookup only stats PATH candidates.
		complete = complete && err == nil
		if err == nil {
			any = true
			if s.Path == "" {
				s.Path = path
			}
		}
	}
	if !any {
		return PackageStatus{}
	}
	root := distributionRoot(s.Path)
	switch name {
	case "go":
		for _, candidate := range []string{root, filepath.Join(root, "libexec"), m.environmentValue("GOROOT")} {
			if candidate != "" && executable(filepath.Join(candidate, "bin/go")) && goVersion.MatchString(nativeGoVersion(candidate)) {
				s.Version = nativeGoVersion(candidate)
				break
			}
		}
		complete = complete && s.Version != ""
	case "jdk":
		for _, candidate := range []string{root, m.environmentValue("JAVA_HOME")} {
			if candidate != "" && executable(filepath.Join(candidate, "bin/java")) && executable(filepath.Join(candidate, "bin/javac")) && jdkVersion(candidate) != "" {
				s.Version = jdkVersion(candidate)
				break
			}
		}
		complete = complete && s.Version != ""
	case "dotnet":
		for _, candidate := range []string{filepath.Dir(resolved(s.Path)), m.environmentValue("DOTNET_ROOT")} {
			if candidate != "" && executable(filepath.Join(candidate, "dotnet")) {
				if version := sdkVersion(candidate); version != "" {
					s.Version = version
					break
				}
			}
		}
		complete = complete && s.Version != ""
	case "rust":
		if nativeProxy(s.Path, "rustup") {
			complete = false
		}
	case "lean":
		if nativeProxy(s.Path, "elan") {
			complete = false
		}
	case "node":
		if version := strings.TrimPrefix(filepath.Base(root), "v"); releaseVersion.MatchString(version) {
			s.Version = version
		}
	case "pi":
		s.Version = nativePackageVersion(s.Path)
	}
	s.Installed, s.Partial = complete, !complete
	if s.Partial {
		s.Detail = "Some commands are on PATH; complete toolchain metadata or dependencies are missing"
	}
	return s
}

// rustup/elan proxies may be hardlinks instead of symlinks. Stat identity avoids
// declaring such a proxy a real compiler when its toolchain is missing.
func nativeProxy(binary, manager string) bool {
	if filepath.Base(resolved(binary)) == manager {
		return true
	}
	binaryInfo, err := os.Stat(binary)
	if err != nil {
		return false
	}
	managerInfo, err := os.Stat(filepath.Join(filepath.Dir(binary), manager))
	return err == nil && os.SameFile(binaryInfo, managerInfo)
}

func (m *manager) environmentValue(key string) string {
	for _, entry := range m.env {
		if strings.HasPrefix(entry, key+"=") {
			return strings.TrimPrefix(entry, key+"=")
		}
	}
	return ""
}
