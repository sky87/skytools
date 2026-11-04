package skytools

import (
	"strings"
	"testing"
)

func TestSelectionResolvesTransitiveDependenciesBeforeDependents(t *testing.T) {
	catalog := []tool{{name: "cli"}, {name: "node"}, {name: "library"}, {name: "other"}}
	requirements := map[string]toolRequirements{"cli": {Tools: []string{"library"}}, "library": {Tools: []string{"node"}}}
	for _, only := range []string{"cli", "cli,library,cli"} {
		selected, err := selectToolsWithRequirements(catalog, only, true, requirements)
		if err != nil || toolNames(selected) != "node,library,cli" {
			t.Fatalf("selection %q: %s %v", only, toolNames(selected), err)
		}
	}
	selected, err := selectToolsWithRequirements(catalog, "cli", false, requirements)
	if err != nil || toolNames(selected) != "cli" {
		t.Fatalf("read-only selection expanded dependencies: %v %v", selected, err)
	}
	for _, only := range []string{"", "all", "all,cli"} {
		selected, err := selectToolsWithRequirements(catalog, only, true, requirements)
		if err != nil || toolNames(selected) != "node,library,cli,other" {
			t.Fatalf("all selection %q: %s %v", only, toolNames(selected), err)
		}
	}
}

func TestSelectionRejectsCyclesMissingDependenciesAndUnknownSelections(t *testing.T) {
	catalog := []tool{{name: "a"}, {name: "b"}}
	for _, tc := range []struct {
		requirements  map[string]toolRequirements
		only, message string
	}{
		{map[string]toolRequirements{"a": {Tools: []string{"b"}}, "b": {Tools: []string{"a"}}}, "a", "cycle"},
		{map[string]toolRequirements{"a": {Tools: []string{"missing"}}}, "a", "absent"},
		{nil, "all,unknown", "unknown"},
		{nil, "a,", "unknown"},
	} {
		if _, err := selectToolsWithRequirements(catalog, tc.only, true, tc.requirements); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("expected %s: %v", tc.message, err)
		}
	}
}
