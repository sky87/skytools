package skytools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func inventoryManager(t testing.TB) *manager {
	t.Helper()
	return &manager{ctx: context.Background(), home: t.TempDir(), env: []string{"PATH=/no-external-tools"}, out: io.Discard, errOut: io.Discard, client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("inventory contacted network"); return nil, nil })}}
}
func inventoryFile(t testing.TB, m *manager, name, content string, mode os.FileMode) {
	t.Helper()
	if err := atomicWrite(m.at(name), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
func inventoryExe(t testing.TB, m *manager, name string) {
	t.Helper()
	inventoryFile(t, m, name, "#!/bin/sh\nprintf invoked > \""+m.at("process-started")+"\"\n", 0755)
}
func populateInventory(t testing.TB, m *manager) {
	t.Helper()
	for _, name := range []string{
		".local/bin/uv", ".cargo/bin/rustup", ".cargo/bin/rustc", ".cargo/bin/cargo", ".rustup/toolchains/stable-test/bin/rustc", ".rustup/toolchains/stable-test/bin/cargo",
		".nvm/versions/node/v24.14.0/bin/node", ".nvm/versions/node/v24.14.0/bin/npm", ".nvm/versions/node/v24.14.0/bin/pnpm", ".bun/bin/bun", ".dotnet/dotnet", ".go/current/bin/go", ".jdk/current/bin/java", ".jdk/current/bin/javac",
		".fzf/bin/fzf", ".local/bin/gh", ".local/bin/opam", ".opam/default/bin/ocaml", ".opam/default/bin/ocamlc", ".elan/bin/elan", ".elan/bin/lean", ".elan/toolchains/leanprover--lean4---v4.25.0/bin/lean",
		".local/bin/claude", ".local/bin/codex", ".opencode/bin/opencode", ".local/share/pnpm/pi", ".local/bin/btop", ".local/bin/tailscale", ".local/bin/tailscaled",
	} {
		inventoryExe(t, m, name)
	}
	if err := link(m.at(".nvm/versions/node/v24.14.0"), m.at(".nvm/current")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node", "npm", "pnpm"} {
		if err := link(m.at(".nvm/current/bin", name), m.at(".local/bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{
		".nvm/nvm.sh": "# native nvm script", ".opam/config": "opam-version: \"2.0\"", ".dotnet/sdk/10.0.100/dotnet.dll": "SDK", ".go/current/VERSION": "go1.27.3\ntime 2026-10-19T00:00:00Z",
		".jdk/current/release": "JAVA_VERSION=\"29.0.1\"\nIMPLEMENTOR=\"Amazon.com Inc.\"\n", ".rustup/settings.toml": "default_toolchain = \"stable-test\"", ".elan/settings.toml": "default_toolchain = 'leanprover/lean4:v4.25.0'",
		".rustup/toolchains/stable-test/lib/rustlib/multirust-channel-manifest.toml": "[pkg.rust]\nversion = \"1.99.0 (test)\"\n",
	} {
		inventoryFile(t, m, name, text, 0644)
	}
	populatePlaywrightInventory(t, m)
}
func inventorySnapshot(t testing.TB, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files[path] = fmt.Sprintf("%v:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
func TestInventoryIsOfflineReadOnlyAndDoesNotExecuteTools(t *testing.T) {
	m := inventoryManager(t)
	populateInventory(t, m)
	before := inventorySnapshot(t, m.home)
	statuses := m.scan(m.tools())
	if len(statuses) != 19 {
		t.Fatalf("got %d packages", len(statuses))
	}
	for _, s := range statuses {
		if !s.Installed || s.Partial || s.Origin != "managed" {
			t.Errorf("unexpected status: %+v", s)
		}
		if s.Channel == "" || s.Path == "" {
			t.Errorf("missing metadata: %+v", s)
		}
	}
	if after := inventorySnapshot(t, m.home); !reflect.DeepEqual(before, after) {
		t.Fatal("inventory changed files")
	}
	if exists(m.at("process-started")) {
		t.Fatal("inventory executed tool")
	}
}
func TestInventoryDetectsPartialToolchainsAndBrokenLinks(t *testing.T) {
	for name, paths := range map[string][]string{"rust": {".cargo/bin/rustup", ".cargo/bin/rustc", ".cargo/bin/cargo"}, "lean": {".elan/bin/elan", ".elan/bin/lean"}, "dotnet": {".dotnet/dotnet"}, "node": {".local/bin/node"}, "ocaml": {".local/bin/opam"}, "jdk": {".jdk/current/bin/java"}, "go": {".go/current/bin/go"}} {
		t.Run(name, func(t *testing.T) {
			m := inventoryManager(t)
			for _, path := range paths {
				inventoryExe(t, m, path)
			}
			s := m.scan([]tool{{name: name}})[0]
			if s.Installed || !s.Partial {
				t.Fatalf("proxy/runtime-only complete: %+v", s)
			}
		})
	}
	m := inventoryManager(t)
	if err := os.MkdirAll(m.at(".go"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(m.at("missing-release"), m.at(".go/current")); err != nil {
		t.Fatal(err)
	}
	if s := m.scan([]tool{{name: "go"}})[0]; s.Installed || !s.Partial {
		t.Fatalf("broken link: %+v", s)
	}
}
func TestInventoryUsesNativeMetadataWithoutSkytoolsMarkers(t *testing.T) {
	m := inventoryManager(t)
	populateInventory(t, m)
	if err := m.ensureJDK(); err != nil {
		t.Fatal(err)
	}
	statuses := m.scan([]tool{{name: "go"}, {name: "jdk"}, {name: "dotnet"}, {name: "rust"}, {name: "lean"}, {name: "node"}})
	for i, v := range []string{"go1.27.3", "29.0.1", "10.0.100", "1.99.0", "4.25.0", "24.14.0"} {
		if statuses[i].Version != v {
			t.Errorf("%s version=%q want %q", statuses[i].Name, statuses[i].Version, v)
		}
	}
	for _, marker := range []string{".sky-tools-provider", ".sky-tools-sha256"} {
		if exists(m.at(".jdk/current", marker)) {
			t.Fatalf("tracking file %s", marker)
		}
	}
}
func TestInventoryRecognizesSystemGroupsAndRejectsLoneRuntimes(t *testing.T) {
	m := inventoryManager(t)
	bin := t.TempDir()
	for _, name := range []string{"uv", "node", "rustc", "cargo", "rustup"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	m.env = []string{"PATH=" + bin}
	statuses := m.scan([]tool{{name: "uv"}, {name: "node"}, {name: "rust"}})
	if !statuses[0].Installed || statuses[0].Origin != "system" {
		t.Fatalf("system uv: %+v", statuses[0])
	}
	if statuses[1].Installed || !statuses[1].Partial {
		t.Fatalf("lone node: %+v", statuses[1])
	}
	if !statuses[2].Installed {
		t.Fatalf("native rust: %+v", statuses[2])
	}
	if err := os.Remove(filepath.Join(bin, "rustc")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(bin, "rustup"), filepath.Join(bin, "rustc")); err != nil {
		t.Fatal(err)
	}
	if s := m.scan([]tool{{name: "rust"}})[0]; s.Installed || !s.Partial {
		t.Fatalf("hardlink proxy: %+v", s)
	}
	for _, name := range []string{"npm", "pnpm"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if s := m.scan([]tool{{name: "node"}})[0]; !s.Installed || s.Origin != "system" {
		t.Fatalf("system node: %+v", s)
	}
}
func TestInventorySelectedMissingDefaultToolchainIsPartial(t *testing.T) {
	m := inventoryManager(t)
	populateInventory(t, m)
	inventoryFile(t, m, ".rustup/settings.toml", "default_toolchain = \"missing\"", 0644)
	inventoryFile(t, m, ".elan/settings.toml", "default_toolchain = \"leanprover/lean4:v99.0.0\"", 0644)
	for _, s := range m.scan([]tool{{name: "rust"}, {name: "lean"}}) {
		if s.Installed || !s.Partial {
			t.Fatalf("missing default: %+v", s)
		}
	}
}
func TestInventoryNeedsDefaultForToolchainProxies(t *testing.T) {
	m := inventoryManager(t)
	populateInventory(t, m)
	for _, path := range []string{".rustup/settings.toml", ".elan/settings.toml"} {
		if err := os.Remove(m.at(path)); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range m.scan([]tool{{name: "rust"}, {name: "lean"}}) {
		if s.Installed || !s.Partial {
			t.Fatalf("proxy without default: %+v", s)
		}
	}
}
func TestNativeMetadataReadsAreBounded(t *testing.T) {
	m := inventoryManager(t)
	inventoryFile(t, m, "huge", strings.Repeat("x", 64*1024+1), 0644)
	if nativeText(m.at("huge")) != "" {
		t.Fatal("accepted oversized metadata")
	}
}
func TestNativeMetadataIgnoresNamedPipes(t *testing.T) {
	m := inventoryManager(t)
	if err := syscall.Mkfifo(m.at("fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if nativeText(m.at("fifo")) != "" {
		t.Fatal("accepted named pipe")
	}
}
func TestInventoryRecognizesSystemRootsBehindWrappers(t *testing.T) {
	m := inventoryManager(t)
	bin, goRoot, javaRoot, dotnetRoot := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for _, path := range []string{filepath.Join(bin, "go"), filepath.Join(bin, "java"), filepath.Join(bin, "javac"), filepath.Join(bin, "dotnet"), filepath.Join(goRoot, "bin/go"), filepath.Join(javaRoot, "bin/java"), filepath.Join(javaRoot, "bin/javac"), filepath.Join(dotnetRoot, "dotnet")} {
		if err := atomicWrite(path, []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for path, text := range map[string]string{filepath.Join(goRoot, "VERSION"): "go1.27.3", filepath.Join(javaRoot, "release"): "JAVA_VERSION=\"29.0.1\"\n", filepath.Join(dotnetRoot, "sdk/10.0.100/dotnet.dll"): "SDK"} {
		if err := atomicWrite(path, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	m.env = []string{"PATH=" + bin, "GOROOT=" + goRoot, "JAVA_HOME=" + javaRoot, "DOTNET_ROOT=" + dotnetRoot}
	for _, s := range m.scan([]tool{{name: "go"}, {name: "jdk"}, {name: "dotnet"}}) {
		if !s.Installed || s.Partial || s.Origin != "system" || s.Version == "" {
			t.Fatalf("system root: %+v", s)
		}
	}
}
func BenchmarkInventoryEmpty(b *testing.B) {
	m := inventoryManager(b)
	tools := m.tools()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.scan(tools)
	}
}
func BenchmarkInventoryPopulated(b *testing.B) {
	m := inventoryManager(b)
	populateInventory(b, m)
	tools := m.tools()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.scan(tools)
	}
}
