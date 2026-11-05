package skytools

import (
	"fmt"
	"io"
	"strings"

	"github.com/sky87/skytools/internal/termui"
)

func packageStateWord(p PackageStatus) string {
	if p.Partial {
		return "Incomplete"
	}
	if p.Installed {
		return "Installed"
	}
	return "Missing"
}

func packageDetail(p PackageStatus) string {
	var parts []string
	if p.Version != "" {
		parts = append(parts, p.Version)
	}
	if p.Path != "" {
		parts = append(parts, p.Path)
	}
	if p.Detail != "" {
		parts = append(parts, p.Detail)
	}
	return strings.Join(parts, " | ")
}

func printInventory(out io.Writer, packages []PackageStatus) {
	fmt.Fprintln(out, "skytools local package inventory (read-only)")
	for _, p := range packages {
		state := strings.ToLower(packageStateWord(p))
		if !p.Installed && !p.Partial {
			state = "not installed / unavailable"
		}
		detail := packageDetail(p)
		if p.Origin == "system" {
			detail = "system PATH | " + detail
		}
		fmt.Fprintf(out, "%-12s %-27s %-7s %s\n", termui.Sanitize(p.Name), state, termui.Sanitize(p.Channel), termui.Sanitize(detail))
	}
	fmt.Fprintln(out, "Use skytools in an interactive terminal to select packages.")
	fmt.Fprintln(out, "Or use: skytools ensure --only <names> | skytools update --only <names>")
	fmt.Fprintln(out, "Use skytools status --versions for explicit version commands.")
}
