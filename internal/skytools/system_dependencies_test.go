package skytools

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDebianDependenciesUseInstalledNativePackagesAndRuntimeABIs(t *testing.T) {
	queryCalls := 0
	provider := debianPackageProvider{architecture: "arm64", query: func(context.Context) (string, error) {
		queryCalls++
		return "curl\tarm64\tinstalled\nca-certificates\tall\tinstalled\nlibicu76\tarm64\tinstalled\nlibssl3t64\tarm64\tinstalled\nlibstdc++6\tamd64\tinstalled\ngit\tarm64\tconfig-files\n", nil
	}}
	report, err := provider.Check(context.Background(), []SystemDependency{{"http-client", []string{"uv"}}, {"certificates", []string{"uv"}}, {"icu-runtime", []string{"dotnet"}}, {"ssl-runtime", []string{"dotnet"}}, {"cxx-runtime", []string{"node"}}, {"git", []string{"node"}}})
	if err != nil {
		t.Fatal(err)
	}
	if queryCalls != 1 || len(report.Missing) != 2 {
		t.Fatalf("queries=%d report=%+v", queryCalls, report)
	}
	if report.InstallCommand != "sudo apt-get install --no-install-recommends git libstdc++6" {
		t.Fatal(report.InstallCommand)
	}
	if strings.Join(report.Missing[0].RequiredBy, ",") != "node" {
		t.Fatal("lost dependent tool")
	}
}

func TestDebianInstallRunsOneDeduplicatedTransactionAndVerifies(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Docker root installer test")
	}
	output := ""
	var commands [][]string
	queries := 0
	p := debianPackageProvider{architecture: "amd64", query: func(context.Context) (string, error) { queries++; return output, nil }}
	p.run = func(_ context.Context, args ...string) error {
		commands = append(commands, append([]string{}, args...))
		if len(commands) == 2 {
			output = "curl\tamd64\tinstalled\ngit\tamd64\tinstalled\n"
		}
		return nil
	}
	deps := []SystemDependency{{"git", []string{"node"}}, {"http-client", []string{"uv", "rust"}}, {"http-client", []string{"node"}}}
	if err := p.Install(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"/usr/bin/apt-get", "update"}, {"/usr/bin/apt-get", "install", "-y", "--no-install-recommends", "curl", "git"}}
	if !reflect.DeepEqual(commands, want) || queries != 2 {
		t.Fatalf("commands=%v queries=%d", commands, queries)
	}
	commands = nil
	if err := p.Install(context.Background(), deps); err != nil || len(commands) != 0 {
		t.Fatalf("not idempotent: %v %v", commands, err)
	}
}

func TestDebianInstallStopsOnFailureAndNeverClaimsMissingPackagesWereInstalled(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Docker root installer test")
	}
	for _, failAt := range []int{1, 2, 0} {
		calls := 0
		p := debianPackageProvider{architecture: "amd64", query: func(context.Context) (string, error) { return "", nil }, run: func(ctx context.Context, args ...string) error {
			calls++
			if calls == failAt {
				return errors.New("fixture failure")
			}
			return nil
		}}
		if err := p.Install(context.Background(), []SystemDependency{{Capability: "git"}}); err == nil {
			t.Fatal("installation failure was ignored")
		}
		if failAt == 1 && calls != 1 {
			t.Fatal("continued after apt update failed")
		}
	}
	p := debianPackageProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Install(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestDependenciesCoverRegistryAndIncludeAutomaticDependencies(t *testing.T) {
	m := testManager(t)
	for _, tool := range m.tools() {
		if _, ok := packageRequirements[tool.name]; !ok {
			t.Errorf("missing prerequisite model for %s", tool.name)
		}
	}
	selected, err := selectTools(m.tools(), "pi", true)
	if err != nil {
		t.Fatal(err)
	}
	deps := dependenciesFor("update", selected, nil)
	found := false
	for _, dep := range deps {
		if dep.Capability == "xz" && strings.Join(dep.RequiredBy, ",") == "node" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing Node prerequisite: %+v", deps)
	}
	deps = dependenciesFor("ensure", []tool{{name: "uv"}}, []PackageStatus{{Installed: true}})
	if len(deps) != 0 {
		t.Fatalf("complete uv unnecessarily requires installer utilities: %+v", deps)
	}
}

type packageProviderFunc func(context.Context, []SystemDependency) (SystemDependencyReport, error)

func (f packageProviderFunc) Check(ctx context.Context, deps []SystemDependency) (SystemDependencyReport, error) {
	return f(ctx, deps)
}

func TestPreflightStopsAllInstallersBeforeMutation(t *testing.T) {
	m := testManager(t)
	checked, installed := false, false
	m.systemProvider = packageProviderFunc(func(ctx context.Context, deps []SystemDependency) (SystemDependencyReport, error) {
		checked = true
		return SystemDependencyReport{Platform: "fixture", InstallCommand: "sudo apt-get install curl", Missing: []MissingSystemDependency{{"http-client", []string{"curl"}, []string{"uv"}}}}, nil
	})
	err := m.perform("ensure", []tool{{name: "uv", label: "uv", ensure: func() error { installed = true; return nil }}}, nil)
	var missing *systemDependenciesError
	if !errors.As(err, &missing) || !checked || installed {
		t.Fatalf("err=%v checked=%v installed=%v", err, checked, installed)
	}
	if exists(m.at(".profile")) {
		t.Fatal("preflight failure configured user shell")
	}
	if !strings.Contains(err.Error(), "apt-get install curl") {
		t.Fatal(err)
	}
}

func TestDebianQueryFailureAndUnknownCapabilityAreErrors(t *testing.T) {
	provider := debianPackageProvider{query: func(context.Context) (string, error) { return "", errors.New("database unreadable") }}
	if _, err := provider.Check(context.Background(), []SystemDependency{{Capability: "git"}}); err == nil {
		t.Fatal("ignored database failure")
	}
	provider.query = func(context.Context) (string, error) { return "", nil }
	if _, err := provider.Check(context.Background(), []SystemDependency{{Capability: "not-implemented"}}); err == nil {
		t.Fatal("ignored unmapped capability")
	}
}

func TestDebianBuildComponentsAndSSLCompatibility(t *testing.T) {
	output := "gcc\tamd64\tinstalled\ng++\tamd64\tinstalled\nmake\tamd64\tinstalled\nlibc6-dev\tamd64\tinstalled\nlibssl1.1\tamd64\tinstalled\n"
	provider := debianPackageProvider{architecture: "amd64", query: func(context.Context) (string, error) { return output, nil }}
	deps := []SystemDependency{{Capability: "c-build"}, {Capability: "ssl-runtime"}}
	report, err := provider.Check(context.Background(), deps)
	if err != nil || len(report.Missing) != 1 || report.Missing[0].Capability != "ssl-runtime" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	output = strings.ReplaceAll(output, "g++\tamd64\tinstalled\n", "") + "libssl3\tamd64\tinstalled\n"
	report, err = provider.Check(context.Background(), deps)
	if err != nil || len(report.Missing) != 1 || report.Missing[0].Capability != "c-build" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestOpamSandboxPrerequisiteFollowsExplicitSetting(t *testing.T) {
	m := testManager(t)
	var sandbox bool
	m.systemProvider = packageProviderFunc(func(_ context.Context, deps []SystemDependency) (SystemDependencyReport, error) {
		sandbox = false
		for _, dep := range deps {
			sandbox = sandbox || dep.Capability == "process-sandbox"
		}
		return SystemDependencyReport{}, nil
	})
	for _, setting := range []string{"", "1"} {
		t.Setenv("SKY_TOOLS_DISABLE_OPAM_SANDBOX", setting)
		if _, err := m.checkSystemDependencies("update", []tool{{name: "ocaml"}}); err != nil {
			t.Fatal(err)
		}
		if sandbox != (setting == "") {
			t.Fatalf("setting=%q sandbox=%v", setting, sandbox)
		}
	}
}
