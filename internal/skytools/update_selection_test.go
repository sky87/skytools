package skytools

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

// updateHome has ripgrep installed, Node partly installed (nvm without Node),
// and nothing else, with no system tools on PATH.
func updateHome(t *testing.T) *manager {
	t.Helper()
	m := testManager(t)
	m.env = setEnv(m.env, "PATH", t.TempDir())
	for path, body := range map[string]string{".local/bin/rg": "#!/bin/sh\n", ".nvm/nvm.sh": "# nvm\n"} {
		if err := atomicWrite(m.at(path), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestUpdateSelectsOnlyInstalledTools(t *testing.T) {
	for _, tc := range []struct {
		only, want string
		skipped    []string
	}{
		{"", "node,ripgrep", nil},
		{"all", "node,ripgrep", nil},
		{"ripgrep,jq,fd", "ripgrep", []string{"jq", "fd"}},
		{"jq", "", []string{"jq"}},
		{"node", "node", nil},
	} {
		t.Run(tc.only, func(t *testing.T) {
			m := updateHome(t)
			got, skipped, err := m.installedForUpdate(m.tools(), tc.only)
			// The order follows the catalog, not the --only list.
			if err != nil || got != tc.want || !reflect.DeepEqual(skipped, tc.skipped) {
				t.Fatalf("got %q skipped %v err %v; want %q skipped %v", got, skipped, err, tc.want, tc.skipped)
			}
		})
	}
}

func TestUpdateStillRejectsUnknownToolNames(t *testing.T) {
	m := updateHome(t)
	if _, _, err := m.installedForUpdate(m.tools(), "ripgrep,ripgerp"); err == nil || !strings.Contains(err.Error(), `unknown tool "ripgerp"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateOfMissingToolsChangesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	var out, errOut strings.Builder
	if code := Run(context.Background(), []string{"update", "--only", "jq,fd"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("exit %d: %s%s", code, out.String(), errOut.String())
	}
	want := "skytools: not installed, so not updated: jq, fd (install with: skytools ensure --only jq,fd)\n" +
		"skytools: nothing to update: none of the selected tools is installed\n"
	if out.String() != want {
		t.Fatalf("output = %q", out.String())
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("update of missing tools changed the home: %v", entries)
	}
}
