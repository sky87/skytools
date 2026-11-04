package skytools

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

//go:embed playwright_runtime/*
var playwrightRuntime embed.FS

func (m *manager) playwrightPrefix() string { return m.at(".local/share/skytools/playwright") }

func playwrightNpmPackage(name string) string {
	if name == "playwright-cli" {
		return "@playwright/cli"
	}
	return "playwright"
}

func (m *manager) playwrightPackage(name string) string {
	return filepath.Join(m.playwrightPrefix(), "lib/node_modules", playwrightNpmPackage(name))
}

type nodePackageMetadata struct {
	Name, Version string
	Bin           json.RawMessage
}

func nodePackageMetadataAt(directory string) nodePackageMetadata {
	var metadata nodePackageMetadata
	_ = json.Unmarshal([]byte(nativeText(filepath.Join(directory, "package.json"))), &metadata)
	return metadata
}

func nodePackageExecutable(directory, name string) string {
	metadata := nodePackageMetadataAt(directory)
	var entries map[string]string
	var entry string
	if json.Unmarshal(metadata.Bin, &entries) == nil {
		entry = entries[name]
	} else {
		_ = json.Unmarshal(metadata.Bin, &entry)
	}
	if entry == "" || filepath.IsAbs(entry) {
		return ""
	}
	entry = filepath.Clean(entry)
	if entry == ".." || strings.HasPrefix(entry, ".."+string(os.PathSeparator)) {
		return ""
	}
	return filepath.Join(directory, entry)
}

// Follow Node's ancestor node_modules search without starting Node. npm may
// deduplicate a dependency or keep it nested under the CLI's package directory.
func resolveNodePackage(from, name string) string {
	for directory := from; ; directory = filepath.Dir(directory) {
		candidate := filepath.Join(directory, "node_modules", name)
		if nodePackageMetadataAt(candidate).Name == name {
			return candidate
		}
		if directory == filepath.Dir(directory) {
			return ""
		}
	}
}

func playwrightDriver(packageDirectory string) (driver, core string) {
	if nodePackageMetadataAt(packageDirectory).Name == "playwright" {
		driver = packageDirectory
	} else {
		driver = resolveNodePackage(packageDirectory, "playwright")
	}
	if driver != "" {
		core = resolveNodePackage(driver, "playwright-core")
	}
	return
}

func (m *manager) installPlaywright(name string, update bool) error {
	if !update && m.managedPlaywrightStatus(name).Installed {
		return nil
	}
	directory := m.playwrightPackage(name)
	metadata := nodePackageMetadataAt(directory)
	driver, core := playwrightDriver(directory)
	validVersion := metadata.Name == playwrightNpmPackage(name) && numericVersion.MatchString(metadata.Version)
	packageReady := validVersion && executable(nodePackageExecutable(directory, name)) && driver != "" && core != "" && regularFile(filepath.Join(driver, "cli.js")) && regularFile(filepath.Join(core, "browsers.json"))
	if name == "playwright" {
		sharedCore := nodePackageMetadataAt(filepath.Join(m.playwrightPrefix(), "lib/node_modules/playwright-core"))
		packageReady = packageReady && sharedCore.Name == "playwright-core" && sharedCore.Version == metadata.Version
	}
	if update || !packageReady {
		version := metadata.Version
		if update || !validVersion {
			latest, err := m.latestVersion(name)
			if err != nil {
				return err
			}
			version = latest.Version
		}
		args := []string{"npm", "install", "--global", "--prefix", m.playwrightPrefix(), "--ignore-scripts", "--no-audit", "--no-fund", playwrightNpmPackage(name) + "@" + version}
		if name == "playwright" {
			args = append(args, "playwright-core@"+version)
		}
		if err := m.run(args...); err != nil {
			return err
		}
	}
	entry := nodePackageExecutable(directory, name)
	if entry == "" || !executable(entry) {
		return fmt.Errorf("%s has no executable entry point", name)
	}
	if err := link(entry, m.at(".local/bin", name)); err != nil {
		return err
	}
	if name == "playwright" {
		for _, file := range []string{"register.mjs", "resolve.mjs", "playwright-node"} {
			data, err := playwrightRuntime.ReadFile("playwright_runtime/" + file)
			if err != nil {
				return err
			}
			destination, mode := filepath.Join(m.playwrightPrefix(), "skytools", file), os.FileMode(0644)
			if file == "playwright-node" {
				destination, mode = m.at(".local/bin/playwright-node"), 0755
			}
			if err := atomicWrite(destination, data, mode); err != nil {
				return err
			}
		}
	} else if err := m.ensurePlaywrightCLIDefaults(); err != nil {
		return err
	}
	driver, core = playwrightDriver(directory)
	if driver == "" || core == "" {
		return fmt.Errorf("%s has no complete Playwright driver", name)
	}
	// Let upstream refresh its browser-client links even when revisions are
	// cached: npm can move a deduplicated driver, and browser GC follows those links.
	if err := m.run("node", filepath.Join(driver, "cli.js"), "install"); err != nil {
		return err
	}
	if !m.managedPlaywrightStatus(name).Installed {
		return fmt.Errorf("%s remains incomplete after installation", name)
	}
	return nil
}

func (m *manager) managedPlaywrightStatus(name string) PackageStatus {
	path, directory := m.at(".local/bin", name), m.playwrightPackage(name)
	s := PackageStatus{Origin: "managed", Path: path}
	_, packageErr := os.Lstat(directory)
	_, binaryErr := os.Lstat(path)
	if packageErr != nil && binaryErr != nil {
		return PackageStatus{}
	}
	s.Partial = true
	metadata := nodePackageMetadataAt(directory)
	s.Version = metadata.Version
	if metadata.Name != playwrightNpmPackage(name) || !numericVersion.MatchString(metadata.Version) || !executable(path) {
		return s
	}
	if _, err := m.lookup("node"); err != nil && !executable(m.at(".local/bin/node")) {
		s.Detail = "Node.js is required"
		return s
	}
	if name == "playwright" {
		sharedCore := nodePackageMetadataAt(filepath.Join(m.playwrightPrefix(), "lib/node_modules/playwright-core"))
		if sharedCore.Name != "playwright-core" || sharedCore.Version != metadata.Version {
			s.Detail = "Shared script library needs repair"
			return s
		}
		if !executable(m.at(".local/bin/playwright-node")) || !regularFile(filepath.Join(m.playwrightPrefix(), "skytools/register.mjs")) || !regularFile(filepath.Join(m.playwrightPrefix(), "skytools/resolve.mjs")) {
			s.Detail = "Script runner needs repair"
			return s
		}
	} else if !regularFile(m.at(".playwright/cli.config.json")) {
		s.Detail = "CLI browser defaults need setup"
		return s
	}
	driver, core := playwrightDriver(directory)
	if driver == "" || core == "" || !regularFile(filepath.Join(driver, "cli.js")) {
		return s
	}
	ready, detail := m.playwrightBrowsersReady(core)
	s.Installed, s.Partial, s.Detail = ready, !ready, detail
	return s
}

// Supply the upstream global default only when absent. Project config, explicit
// CLI options, and existing user config keep their usual upstream precedence.
func (m *manager) ensurePlaywrightCLIDefaults() error {
	target := m.at(".playwright/cli.config.json")
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".skytools-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString("{\"browser\":{\"browserName\":\"chromium\"}}\n")
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Link(file.Name(), target); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

type playwrightBrowser struct {
	Name, Revision    string
	InstallByDefault  bool
	RevisionOverrides map[string]string
}

func (m *manager) playwrightCache(core string) string {
	if configured := m.environmentValue("PLAYWRIGHT_BROWSERS_PATH"); configured != "" {
		if configured == "0" {
			return filepath.Join(core, ".local-browsers")
		}
		if filepath.IsAbs(configured) {
			return configured
		}
		return filepath.Join(m.home, configured)
	}
	if runtime.GOOS == "darwin" {
		return m.at("Library/Caches/ms-playwright")
	}
	if cache := m.environmentValue("XDG_CACHE_HOME"); filepath.IsAbs(cache) {
		return filepath.Join(cache, "ms-playwright")
	}
	return m.at(".cache/ms-playwright")
}

func playwrightHostPlatform() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	if runtime.GOOS == "darwin" {
		match := regexp.MustCompile(`<key>ProductVersion</key>\s*<string>([0-9]+)`).FindStringSubmatch(nativeText("/System/Library/CoreServices/SystemVersion.plist"))
		if len(match) == 2 {
			platform := "mac" + match[1]
			if arch == "arm64" {
				platform += "-arm64"
			}
			return platform
		}
		return ""
	}
	id, version := nativeValue("/etc/os-release", "ID"), nativeValue("/etc/os-release", "VERSION_ID")
	if id == "debian" {
		version = strings.Split(version, ".")[0]
	}
	return id + version + "-" + arch
}

func browserExecutableCandidates(name string) []string {
	if runtime.GOOS == "darwin" {
		switch name {
		case "chromium":
			return []string{"chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing", "chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing", "chrome-mac/Chromium.app/Contents/MacOS/Chromium"}
		case "chromium-headless-shell":
			return []string{"chrome-headless-shell-mac-arm64/chrome-headless-shell", "chrome-headless-shell-mac-x64/chrome-headless-shell", "chrome-mac/headless_shell"}
		case "firefox":
			return []string{"firefox/Nightly.app/Contents/MacOS/firefox"}
		case "webkit":
			return []string{"pw_run.sh"}
		case "ffmpeg":
			return []string{"ffmpeg-mac"}
		}
	}
	switch name {
	case "chromium":
		return []string{"chrome-linux64/chrome", "chrome-linux-arm64/chrome", "chrome-linux/chrome"}
	case "chromium-headless-shell":
		return []string{"chrome-headless-shell-linux64/chrome-headless-shell", "chrome-headless-shell-linux-arm64/chrome-headless-shell", "chrome-linux/headless_shell"}
	case "firefox":
		return []string{"firefox/firefox"}
	case "webkit":
		return []string{"pw_run.sh"}
	case "ffmpeg":
		return []string{"ffmpeg-linux"}
	}
	return nil
}

func (m *manager) playwrightBrowsersReady(core string) (bool, string) {
	var manifest struct{ Browsers []playwrightBrowser }
	if json.Unmarshal([]byte(nativeText(filepath.Join(core, "browsers.json"))), &manifest) != nil {
		return false, "Browser metadata is missing"
	}
	cache, platform := m.playwrightCache(core), playwrightHostPlatform()
	engines := map[string]bool{}
	for _, browser := range manifest.Browsers {
		if !browser.InstallByDefault {
			continue
		}
		revision := browser.Revision
		if override := browser.RevisionOverrides[platform]; override != "" {
			revision = override
		}
		if revision == "" || strings.Trim(revision, "0123456789") != "" {
			return false, "Invalid browser revision metadata"
		}
		directory := filepath.Join(cache, strings.ReplaceAll(browser.Name, "-", "_")+"-"+revision)
		ready := false
		for _, candidate := range browserExecutableCandidates(browser.Name) {
			ready = ready || executable(filepath.Join(directory, candidate))
		}
		if !ready || !regularFile(filepath.Join(directory, "INSTALLATION_COMPLETE")) {
			return false, "Missing browser installation: " + browser.Name
		}
		engines[browser.Name] = true
	}
	if !engines["chromium"] || !engines["firefox"] || !engines["webkit"] {
		return false, "Browser engine metadata is incomplete"
	}
	return true, "Chromium, Firefox, and WebKit are available"
}
