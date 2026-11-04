package skytools

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Browser dependencies are upstream data, not Go dependencies. See the source
// attribution and license alongside the generated platform mappings.
//
//go:embed playwrightdata/debian.json
var playwrightDependenciesJSON []byte

var playwrightDependencyTables = sync.OnceValues(func() (map[string][]string, error) {
	var tables map[string][]string
	err := json.Unmarshal(playwrightDependenciesJSON, &tables)
	return tables, err
})

func (p debianPackageProvider) playwrightPackages() ([]string, error) {
	if p.architecture != "amd64" && p.architecture != "arm64" {
		return nil, fmt.Errorf("Playwright dependencies do not support architecture %s", p.architecture)
	}
	version := p.version
	if p.distribution == "debian" {
		version = strings.Split(version, ".")[0]
	}
	tables, err := playwrightDependencyTables()
	if err != nil {
		return nil, fmt.Errorf("read browser dependency data: %w", err)
	}
	packages := tables[p.distribution+"-"+version]
	if len(packages) == 0 {
		return nil, fmt.Errorf("Playwright dependencies are unsupported on %s %s; supported: Debian 12/13 and Ubuntu 22.04/24.04/26.04", p.distribution, p.version)
	}
	return packages, nil
}
