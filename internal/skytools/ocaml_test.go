package skytools

import (
	"os"
	"strings"
	"testing"
)

// A fake opam with a complete default switch, logging its arguments.
func fakeOpamRoot(t *testing.T, m *manager) string {
	t.Helper()
	log := m.at("opam.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\n[ \"$1 $2\" = 'switch list' ] && echo default\nexit 0\n"
	for path, body := range map[string]string{
		".local/bin/opam":          script,
		".opam/config":             "opam-version: \"2.0\"\n",
		".opam/default/bin/ocaml":  "#!/bin/sh\n",
		".opam/default/bin/ocamlc": "#!/bin/sh\n",
	} {
		if err := atomicWrite(m.at(path), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

func TestEnsureOCamlClearsSandboxWrappersOnlyWhenSandboxingIsDisabled(t *testing.T) {
	for _, setting := range []string{"1", ""} {
		t.Run("disabled="+setting, func(t *testing.T) {
			t.Setenv("SKY_TOOLS_DISABLE_OPAM_SANDBOX", setting)
			m := testManager(t)
			log := fakeOpamRoot(t, m)
			if err := m.ensureOCaml(); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(log)
			calls := string(data)
			cleared := strings.Count(calls, "option --global wrap-")
			for _, field := range []string{"build", "install", "remove"} {
				if setting == "1" && !strings.Contains(calls, "option --global wrap-"+field+"-commands=[]\n") {
					t.Fatalf("did not clear wrap-%s-commands:\n%s", field, calls)
				}
			}
			if (setting == "1") != (cleared == 3) || (setting == "" && cleared != 0) {
				t.Fatalf("cleared %d wrappers:\n%s", cleared, calls)
			}
		})
	}
}
