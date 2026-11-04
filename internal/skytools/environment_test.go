package skytools

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestRuntimeRootsPreserveSystemConfigurationUntilManagedInstallExists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for key, value := range map[string]string{"GOROOT": "/system/go", "JAVA_HOME": "/system/java", "DOTNET_ROOT": "/system/dotnet"} {
		t.Setenv(key, value)
	}
	m, err := newManager(context.Background(), io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	get := func(env []string, key string) string {
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				return strings.TrimPrefix(entry, key+"=")
			}
		}
		return ""
	}
	for key, value := range map[string]string{"GOROOT": "/system/go", "JAVA_HOME": "/system/java", "DOTNET_ROOT": "/system/dotnet"} {
		if got := get(m.commandEnvironment(), key); got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
	output, err := m.output("sh", "-c", shellEnv+"\nprintf '%s|%s|%s' \"$GOROOT\" \"$JAVA_HOME\" \"$DOTNET_ROOT\"")
	if err != nil || output != "/system/go|/system/java|/system/dotnet" {
		t.Fatalf("shell changed system roots: %q, %v", output, err)
	}
	for _, root := range []struct{ key, directory, binary string }{
		{"GOROOT", ".go/current", "bin/go"},
		{"JAVA_HOME", ".jdk/current", "bin/java"},
		{"DOTNET_ROOT", ".dotnet", "dotnet"},
	} {
		if err := atomicWrite(m.at(root.directory, root.binary), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if got := get(m.commandEnvironment(), root.key); got != m.at(root.directory) {
			t.Fatalf("new managed %s = %q", root.key, got)
		}
	}
	output, err = m.output("sh", "-c", shellEnv+"\nprintf '%s|%s|%s' \"$GOROOT\" \"$JAVA_HOME\" \"$DOTNET_ROOT\"")
	if err != nil || output != m.at(".go/current")+"|"+m.at(".jdk/current")+"|"+m.at(".dotnet") {
		t.Fatalf("shell did not activate managed roots: %q, %v", output, err)
	}
}
