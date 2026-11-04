package skytools

import (
	"fmt"
	"strings"
)

// Resolve declarative tool dependencies before installers run. Stable catalog
// ordering is retained where possible, with prerequisites always emitted first.
func selectToolsWithRequirements(all []tool, only string, dependencies bool, requirements map[string]toolRequirements) ([]tool, error) {
	available := make(map[string]tool, len(all))
	for _, t := range all {
		available[t.name] = t
	}
	wanted, includeAll := map[string]bool{}, only == ""
	if only != "" {
		for _, name := range strings.Split(only, ",") {
			name = strings.TrimSpace(name)
			if name == "all" {
				includeAll = true
				continue
			}
			if _, ok := available[name]; !ok {
				return nil, fmt.Errorf("unknown tool %q; run skytools list", name)
			}
			wanted[name] = true
		}
	}
	state := map[string]int{}
	var selected []tool
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 2 {
			return nil
		}
		if state[name] == 1 {
			return fmt.Errorf("tool dependency cycle involving %s", name)
		}
		t, ok := available[name]
		if !ok {
			return fmt.Errorf("required tool %q is absent from the catalog", name)
		}
		state[name] = 1
		if dependencies {
			for _, dependency := range requirements[name].Tools {
				if err := visit(dependency); err != nil {
					return fmt.Errorf("%s requires %s: %w", name, dependency, err)
				}
			}
		}
		state[name] = 2
		selected = append(selected, t)
		return nil
	}
	for _, t := range all {
		if includeAll || wanted[t.name] {
			if err := visit(t.name); err != nil {
				return nil, err
			}
		}
	}
	return selected, nil
}
