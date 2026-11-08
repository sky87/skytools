package skytools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCompleter is a managed executable that prints a completion script for
// its first shell argument and counts its runs.
func fakeCompleter(t *testing.T, m *manager, relative, body string) string {
	t.Helper()
	runs := m.at(filepath.Base(relative) + ".runs")
	script := "#!/bin/sh\necho run >> '" + runs + "'\n" + body
	if err := atomicWrite(m.at(relative), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return runs
}

func runCount(path string) int {
	data, _ := os.ReadFile(path)
	return strings.Count(string(data), "run\n")
}

func readCompletion(t *testing.T, m *manager, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(m.at(completionDir), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWriteCompletionsGeneratesOnceAndRegeneratesChangedExecutables(t *testing.T) {
	m := testManager(t)
	fd := ".local/bin/fd"
	runs := fakeCompleter(t, m, fd, `echo "warning on stderr" >&2
case $2 in bash) echo 'complete -F _fd fd' ;; zsh) printf '#compdef fd\n_fd() { :; }\n' ;; esac
`)
	if err := m.writeCompletions(); err != nil {
		t.Fatal(err)
	}
	bash, zsh := readCompletion(t, m, "bash/fd"), readCompletion(t, m, "zsh/_fd")
	if !strings.HasPrefix(bash, "complete -F _fd fd\n"+completionStampPrefix) || !strings.HasPrefix(zsh, "#compdef fd\n_fd") {
		t.Fatalf("bash=%q zsh=%q", bash, zsh)
	}
	if strings.Contains(bash+zsh, "warning") {
		t.Fatal("standard error ended up in a script")
	}
	if err := m.writeCompletions(); err != nil || runCount(runs) != 2 {
		t.Fatalf("regenerated an unchanged executable: runs=%d err=%v", runCount(runs), err)
	}
	// An update replaces the executable.
	fakeCompleter(t, m, fd, "echo 'complete -F _fd2 fd'\n")
	if err := m.writeCompletions(); err != nil || runCount(runs) != 4 || !strings.HasPrefix(readCompletion(t, m, "bash/fd"), "complete -F _fd2 fd") {
		t.Fatalf("did not regenerate: runs=%d err=%v", runCount(runs), err)
	}
}

func TestWriteCompletionsRemovesScriptsOfMissingOrFailingTools(t *testing.T) {
	m := testManager(t)
	fakeCompleter(t, m, ".local/bin/rg", "echo 'complete -F _rg rg'\n")
	fakeCompleter(t, m, ".local/bin/delta", "echo 'complete -F _delta delta'\n")
	var log strings.Builder
	m.out = &log
	if err := m.writeCompletions(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.at(completionDir), "bash", "old-tool"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	os.Remove(m.at(".local/bin/rg"))
	fakeCompleter(t, m, ".local/bin/delta", "echo 'unknown flag' >&2\nexit 2\n")
	if err := m.writeCompletions(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bash/rg", "zsh/_rg", "bash/delta", "zsh/_delta", "bash/old-tool"} {
		if exists(filepath.Join(m.at(completionDir), name)) {
			t.Fatalf("kept %s", name)
		}
	}
	if !strings.Contains(log.String(), "no bash completion for delta: exit status 2: unknown flag") {
		t.Fatalf("log = %q", log.String())
	}
}

func TestWriteCompletionsCompletesTheSgLinkAsAstGrep(t *testing.T) {
	m := testManager(t)
	fakeCompleter(t, m, ".local/bin/ast-grep", `case $2 in
bash) printf '_ast__grep() { :; }\nif true; then\n    complete -F _ast__grep -o default ast-grep\nfi\n' ;;
zsh) printf '#compdef ast-grep\n_ast-grep() { :; }\n' ;;
esac
`)
	if err := m.writeCompletions(); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(m.at(completionDir), "bash/sg")) {
		t.Fatal("completed sg without the sg link")
	}
	if err := link("ast-grep", m.at(".local/bin/sg")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(m.at(completionDir), "bash/ast-grep"))
	os.Remove(filepath.Join(m.at(completionDir), "zsh/_ast-grep"))
	if err := m.writeCompletions(); err != nil {
		t.Fatal(err)
	}
	bash, zsh := readCompletion(t, m, "bash/ast-grep"), readCompletion(t, m, "zsh/_ast-grep")
	if !strings.Contains(bash, "    complete -F _ast__grep -o default ast-grep sg\n") || !strings.HasPrefix(zsh, "#compdef ast-grep sg\n") {
		t.Fatalf("bash=%q zsh=%q", bash, zsh)
	}
	if got := readCompletion(t, m, "bash/sg"); got != ". \"${BASH_SOURCE[0]%/*}/ast-grep\"\n" {
		t.Fatalf("sg = %q", got)
	}
}

func TestWriteCompletionsCopiesCargoScriptsFromTheActiveToolchain(t *testing.T) {
	m := testManager(t)
	for _, toolchain := range []string{"one", "two"} {
		for file, body := range map[string]string{"etc/bash_completion.d/cargo": "complete cargo " + toolchain, "share/zsh/site-functions/_cargo": "#compdef cargo\n# " + toolchain} {
			if err := atomicWrite(m.at("toolchains", toolchain, file), []byte(body+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, toolchain := range []string{"one", "two"} {
		fakeCompleter(t, m, ".cargo/bin/rustc", "echo '"+m.at("toolchains", toolchain)+"'\n")
		if err := m.writeCompletions(); err != nil {
			t.Fatal(err)
		}
		if bash, zsh := readCompletion(t, m, "bash/cargo"), readCompletion(t, m, "zsh/_cargo"); !strings.HasPrefix(bash, "complete cargo "+toolchain+"\n") || !strings.HasPrefix(zsh, "#compdef cargo\n# "+toolchain+"\n") {
			t.Fatalf("bash=%q zsh=%q", bash, zsh)
		}
	}
}

// The bash loader completes skytools itself and loads a tool's script on its
// first Tab.
func TestBashCompletionLoadsScriptsLazilyAndCompletesSkytools(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	skytools := "#!/bin/sh\n[ \"$1\" = list ] && printf 'node            nvm\\nocaml           opam\\nopencode        OpenCode\\n'\n"
	if err := atomicWrite(filepath.Join(bin, "skytools"), []byte(skytools), 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(home, completionDir, "bash/fd"), []byte("_fd() { COMPREPLY=(--hidden); }\ncomplete -F _fd fd\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "completion.bash"), []byte(bashCompletion), 0644); err != nil {
		t.Fatal(err)
	}
	script := `. "$HOME/completion.bash"
complete -p fd
_skytools_complete_load fd; echo "load=$?"
complete -p fd
complete() { COMP_WORDS=("$@"); COMP_CWORD=$(($# - 1)); COMPREPLY=(); _skytools; echo "${COMPREPLY[*]}"; }
complete skytools up
complete skytools update --only o
complete skytools update --only node,oc
complete skytools update --only = no
complete skytools status --v
complete skytools list --only n
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	want := `complete -F _skytools_complete_load fd
load=124
complete -F _fd fd
update
ocaml opencode
node,ocaml
node
--versions

`
	if string(output) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", output, want)
	}
}

func TestShellSetupWritesCompletionLoaders(t *testing.T) {
	m := testManager(t)
	if err := m.setupShell(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"completion.bash": bashCompletion, "completion.zsh": zshCompletion} {
		if data, err := os.ReadFile(m.at(".config/skytools", name)); err != nil || string(data) != want {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !strings.Contains(shellEnv, `. "$HOME/.config/skytools/completion.$_skytools_shell"`) {
		t.Fatal("env.sh does not load completion")
	}
}
