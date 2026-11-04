package skytools

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sky87/skytools/internal/termui"
)

var outputVersion = regexp.MustCompile(`(?:^|[\s"v])([0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9]+)*)(?:[\s"(+]|$)`)

func (m *manager) installedVersion(ctx context.Context, p PackageStatus) (string, error) {
	if p.Version != "" {
		return cleanVersion(p.Version), nil
	}
	if !p.Installed {
		return "", fmt.Errorf("installation is incomplete")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	binary, args := p.Path, []string{"--version"}
	switch p.Name {
	case "rust":
		if p.Origin == "managed" {
			binary = filepath.Join(m.rustToolchain(), "bin/rustc")
		}
	case "lean":
		if p.Origin == "managed" {
			root, _ := m.leanToolchain()
			binary = filepath.Join(root, "bin/lean")
		}
	case "ocaml":
		if p.Origin == "managed" {
			binary = m.at(".opam/default/bin/ocaml")
		} else {
			binary, _ = m.lookup("ocaml")
		}
		args = []string{"-version"}
	case "go":
		args = []string{"version"}
	case "jdk":
		args = []string{"-version"}
	}
	if binary == "" || !executable(binary) {
		return "", fmt.Errorf("installed executable unavailable")
	}
	cmd, err := m.command(ctx, append([]string{binary}, args...)...)
	if err != nil {
		return "", err
	}
	var capture versionOutput
	cmd.Stdout, cmd.Stderr = &capture, &capture
	err = cmd.Run()
	output := capture.String()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("%s version query failed: %w", p.Name, err)
	}
	// Most tools prefix their version with their name. Go uses go1.x.y.
	output = strings.ReplaceAll(output, "go version go", "go version ")
	output = termui.Sanitize(output)
	match := outputVersion.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("unrecognized %s version output", p.Name)
	}
	return match[1], nil
}

type versionOutput struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (v *versionOutput) Write(data []byte) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	count := min(len(data), max(0, 64*1024-v.data.Len()))
	v.data.Write(data[:count])
	return len(data), nil
}
func (v *versionOutput) String() string { v.mu.Lock(); defer v.mu.Unlock(); return v.data.String() }
