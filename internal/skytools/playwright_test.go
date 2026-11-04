package skytools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func populatePlaywrightInventory(t testing.TB, m *manager) {
	t.Helper()
	prefix := ".local/share/skytools/playwright"
	for path, metadata := range map[string]string{
		".playwright/cli.config.json":                             `{"browser":{"browserName":"chromium"}}`,
		prefix + "/lib/node_modules/playwright/package.json":      `{"name":"playwright","version":"1.64.1"}`,
		prefix + "/lib/node_modules/@playwright/cli/package.json": `{"name":"@playwright/cli","version":"0.1.23"}`,
		prefix + "/lib/node_modules/playwright-core/package.json": `{"name":"playwright-core","version":"1.64.1"}`,
		prefix + "/skytools/register.mjs":                         "// fixture",
		prefix + "/skytools/resolve.mjs":                          "// fixture",
	} {
		inventoryFile(t, m, path, metadata, 0644)
	}
	for _, path := range []string{".local/bin/playwright", ".local/bin/playwright-cli", ".local/bin/playwright-node", prefix + "/lib/node_modules/playwright/cli.js"} {
		inventoryExe(t, m, path)
	}
	core := filepath.Join(m.playwrightPrefix(), "lib/node_modules/playwright-core")
	populatePlaywrightBrowserFixture(t, m, core, "1", true)
}

func TestPlaywrightDefaultsPreserveUserConfig(t *testing.T) {
	m := inventoryManager(t)
	if err := m.ensurePlaywrightCLIDefaults(); err != nil {
		t.Fatal(err)
	}
	file := m.at(".playwright/cli.config.json")
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), `"browserName":"chromium"`) {
		t.Fatal(string(data))
	}
	custom := []byte(`{"browser":{"browserName":"firefox"}}`)
	if err := atomicWrite(file, custom, 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.ensurePlaywrightCLIDefaults(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(file)
	if string(data) != string(custom) {
		t.Fatal("overwrote existing user config")
	}
	entries, _ := filepath.Glob(m.at(".playwright/.skytools-*"))
	if len(entries) != 0 {
		t.Fatal("left configuration staging files")
	}
}

func populatePlaywrightBrowserFixture(t testing.TB, m *manager, core, revision string, installed bool) {
	t.Helper()
	var manifest struct{ Browsers []playwrightBrowser }
	for _, name := range []string{"chromium", "chromium-headless-shell", "firefox", "webkit", "ffmpeg"} {
		manifest.Browsers = append(manifest.Browsers, playwrightBrowser{Name: name, Revision: revision, InstallByDefault: true})
		if installed {
			directory := filepath.Join(m.playwrightCache(core), strings.ReplaceAll(name, "-", "_")+"-"+revision)
			if err := atomicWrite(filepath.Join(directory, "INSTALLATION_COMPLETE"), nil, 0644); err != nil {
				t.Fatal(err)
			}
			if err := atomicWrite(filepath.Join(directory, browserExecutableCandidates(name)[0]), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	data, _ := json.Marshal(manifest)
	if err := atomicWrite(filepath.Join(core, "browsers.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestPlaywrightDetectionRequiresBrowserRevisionsAndItsScriptRunner(t *testing.T) {
	m := inventoryManager(t)
	inventoryExe(t, m, ".local/bin/node")
	populatePlaywrightInventory(t, m)
	for _, name := range []string{"playwright", "playwright-cli"} {
		if s := m.managedStatus(name); !s.Installed || s.Version == "" {
			t.Fatalf("%s: %+v", name, s)
		}
	}
	if err := os.Remove(m.at(".local/bin/playwright-node")); err != nil {
		t.Fatal(err)
	}
	if s := m.managedStatus("playwright"); s.Installed || !s.Partial {
		t.Fatalf("missing runner accepted: %+v", s)
	}
	core := filepath.Join(m.playwrightPrefix(), "lib/node_modules/playwright-core")
	if err := os.Remove(filepath.Join(m.playwrightCache(core), "firefox-1/INSTALLATION_COMPLETE")); err != nil {
		t.Fatal(err)
	}
	if s := m.managedStatus("playwright-cli"); s.Installed || !s.Partial {
		t.Fatalf("missing browser accepted: %+v", s)
	}
}

func TestPlaywrightCLIDetectionUsesItsOwnNestedDriver(t *testing.T) {
	m := inventoryManager(t)
	inventoryExe(t, m, ".local/bin/node")
	populatePlaywrightInventory(t, m)
	cli := m.playwrightPackage("playwright-cli")
	driver, core := filepath.Join(cli, "node_modules/playwright"), filepath.Join(cli, "node_modules/playwright-core")
	for directory, name := range map[string]string{driver: "playwright", core: "playwright-core"} {
		if err := atomicWrite(filepath.Join(directory, "package.json"), []byte(`{"name":"`+name+`","version":"1.65.0-alpha-fixture"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicWrite(filepath.Join(driver, "cli.js"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	populatePlaywrightBrowserFixture(t, m, core, "2", false)
	if s := m.managedStatus("playwright-cli"); s.Installed || !s.Partial {
		t.Fatalf("CLI reused wrong driver: %+v", s)
	}
	if s := m.managedStatus("playwright"); !s.Installed {
		t.Fatalf("CLI changed standalone library state: %+v", s)
	}
	populatePlaywrightBrowserFixture(t, m, core, "2", true)
	if s := m.managedStatus("playwright-cli"); !s.Installed {
		t.Fatalf("CLI browser revisions missing: %+v", s)
	}
}

func TestPlaywrightDependenciesUseDistributionTablesAndVirtualPackages(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		p := debianPackageProvider{distribution: "debian", version: "13", architecture: architecture}
		packages, err := p.playwrightPackages()
		if err != nil || len(packages) < 50 {
			t.Fatalf("dependency table: %v %v", packages, err)
		}
		var lines []string
		for _, name := range packages {
			if name == "libasound2" {
				continue
			}
			provided := ""
			if name == "libasound2t64" {
				provided = "libasound2 (= 1.2.14)"
			}
			lines = append(lines, strings.Join([]string{name, architecture, "installed", provided}, "\t"))
		}
		output := strings.Join(lines, "\n")
		p.query = func(context.Context) (string, error) { return output, nil }
		deps := []SystemDependency{{Capability: "playwright-browsers", RequiredBy: []string{"playwright"}}}
		report, err := p.Check(context.Background(), deps)
		if err != nil || len(report.Missing) != 0 {
			t.Fatalf("provided package not recognized: %+v %v", report, err)
		}
		output = strings.ReplaceAll(output, "libgtk-4-1\t"+architecture+"\tinstalled", "libgtk-4-1\t"+architecture+"\tunpacked")
		report, err = p.Check(context.Background(), deps)
		if err != nil || len(report.Missing) != 1 || strings.Join(report.Missing[0].Packages, ",") != "libgtk-4-1" {
			t.Fatalf("missing browser library: %+v %v", report, err)
		}
	}
	for _, distribution := range []struct{ name, version string }{{"debian", "12"}, {"ubuntu", "22.04"}, {"ubuntu", "24.04"}, {"ubuntu", "26.04"}} {
		p := debianPackageProvider{distribution: distribution.name, version: distribution.version, architecture: "arm64"}
		if _, err := p.playwrightPackages(); err != nil {
			t.Fatal(err)
		}
	}
	p := debianPackageProvider{distribution: "debian", version: "99", architecture: "arm64"}
	if _, err := p.playwrightPackages(); err == nil {
		t.Fatal("guessed unsupported platform dependencies")
	}
}

func TestPlaywrightCLISelectionIncludesLibraryAndNode(t *testing.T) {
	m := inventoryManager(t)
	tools, err := selectTools(m.tools(), "playwright-cli", true)
	if err != nil || toolNames(tools) != "node,playwright,playwright-cli" {
		t.Fatalf("selection=%s err=%v", toolNames(tools), err)
	}
}
