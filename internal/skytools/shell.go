package skytools

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed env.sh
var shellEnv string

const shellMarker = "# skytools environment"
const shellHook = shellMarker + "\n[ ! -r \"$HOME/.config/skytools/env.sh\" ] || . \"$HOME/.config/skytools/env.sh\"\n"

func (m *manager) shellProfiles() ([]string, error) {
	profiles := []string{".profile", ".bashrc", ".zshrc"}
	// Bash ignores .profile if either of these files already exists.
	for _, name := range []string{".bash_profile", ".bash_login"} {
		if _, err := os.Stat(m.at(name)); err == nil {
			profiles = append(profiles, name)
			break
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return profiles, nil
}

// profilesWithoutHook lets Review say what a run will change in shell files. It
// only reads them.
func (m *manager) profilesWithoutHook() []string {
	profiles, err := m.shellProfiles()
	if err != nil {
		return nil
	}
	var missing []string
	for _, profile := range profiles {
		data, err := os.ReadFile(m.at(profile))
		if err != nil || !strings.Contains(string(data), shellMarker) {
			missing = append(missing, "~/"+profile)
		}
	}
	return missing
}

func (m *manager) setupShell() error {
	if err := os.MkdirAll(m.at(".local/bin"), 0755); err != nil {
		return err
	}
	if err := atomicWrite(m.at(".config/skytools/env.sh"), []byte(shellEnv), 0644); err != nil {
		return err
	}
	profiles, err := m.shellProfiles()
	if err != nil {
		return err
	}
	for _, profile := range profiles {
		name := m.at(profile)
		// Respect existing dotfile symlinks, but never mutate a target outside HOME.
		if resolved, err := filepath.EvalSymlinks(name); err == nil {
			rel, err := filepath.Rel(m.home, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("shell profile %s points outside HOME; add the environment hook manually", name)
			}
			name = resolved
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := os.ReadFile(name)
		mode := os.FileMode(0644)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if strings.Contains(string(data), shellMarker) {
			continue
		}
		if info, err := os.Stat(name); err == nil {
			mode = info.Mode().Perm()
			backup := name + ".skytools.bak"
			if _, err := os.Lstat(backup); os.IsNotExist(err) {
				if err := atomicWrite(backup, data, mode); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, []byte("\n"+shellHook)...)
		if err := atomicWrite(name, data, mode); err != nil {
			return err
		}
	}
	return nil
}
