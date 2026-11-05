package skytools

import (
	"fmt"
	"io"
	"strings"

	"github.com/sky87/skytools/internal/termui"
)

// inventoryState uses the same words as the dashboard. Scripts may rely on them:
// scripts/check-status.sh looks for "missing" and "incomplete".
func inventoryState(p PackageStatus) string {
	switch {
	case p.Partial:
		return "incomplete"
	case p.Installed:
		return "installed"
	}
	return "missing"
}

func inventoryCounts(packages []PackageStatus) string {
	counts := map[string]int{}
	for _, p := range packages {
		counts[inventoryState(p)]++
	}
	var parts []string
	for _, state := range []string{"installed", "incomplete", "missing"} {
		if counts[state] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[state], state))
		}
	}
	return strings.Join(parts, ", ")
}

func inventoryWhere(home string, p PackageStatus) string {
	where := p.Path
	if home != "" && (where == home || strings.HasPrefix(where, home+"/")) {
		where = "~" + strings.TrimPrefix(where, home)
	}
	if !p.Installed && !p.Partial {
		return ""
	}
	if p.Origin == "system" && p.Installed {
		where += " (system PATH)"
	}
	if p.Partial {
		where = strings.TrimSpace(where + " " + p.Detail)
	} else if _, extra, ok := strings.Cut(p.Detail, " | "); ok {
		where += " " + extra
	}
	return where
}

// printInventory is the filesystem-only output of status and of a piped skytools.
// Columns are sized from the data, so a long name cannot push the others out.
func printInventory(out io.Writer, home string, packages []PackageStatus) {
	fmt.Fprintln(out, "skytools local package inventory (read-only)")
	if counts := inventoryCounts(packages); counts != "" {
		fmt.Fprintln(out, counts)
	}
	fmt.Fprintln(out)
	nameWidth, versionWidth := 0, 0
	for _, p := range packages {
		nameWidth = max(nameWidth, termui.Width(p.Name))
		versionWidth = max(versionWidth, termui.Width(p.Version))
	}
	for _, p := range packages {
		line := fmt.Sprintf("%-*s  %-10s  %-*s  %s", nameWidth, termui.Sanitize(p.Name), inventoryState(p), versionWidth, termui.Sanitize(p.Version), termui.Sanitize(inventoryWhere(home, p)))
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run skytools in a terminal for the dashboard. status --versions asks each tool.")
}
