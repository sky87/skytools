package skytools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

type debianRequirement struct {
	packages []string
	all      []string
	pattern  string
	install  string
}

var debianRequirements = map[string]debianRequirement{
	"certificates":     {packages: []string{"ca-certificates"}, install: "ca-certificates"},
	"http-client":      {packages: []string{"curl"}, install: "curl"},
	"bash":             {packages: []string{"bash"}, install: "bash"},
	"git":              {packages: []string{"git"}, install: "git"},
	"tar":              {packages: []string{"tar"}, install: "tar"},
	"gzip":             {packages: []string{"gzip"}, install: "gzip"},
	"xz":               {packages: []string{"xz-utils"}, install: "xz-utils"},
	"unzip":            {packages: []string{"unzip"}, install: "unzip"},
	"c-build":          {packages: []string{"build-essential"}, all: []string{"gcc", "g++", "make", "libc6-dev"}, install: "build-essential"},
	"m4":               {packages: []string{"m4"}, install: "m4"},
	"pkg-config":       {packages: []string{"pkg-config", "pkgconf"}, install: "pkg-config"},
	"gmp-dev":          {packages: []string{"libgmp-dev"}, install: "libgmp-dev"},
	"gmp-runtime":      {packages: []string{"libgmp10"}, install: "libgmp10"},
	"process-sandbox":  {packages: []string{"bubblewrap"}, install: "bubblewrap"},
	"cxx-runtime":      {packages: []string{"libstdc++6"}, install: "libstdc++6"},
	"libc-runtime":     {packages: []string{"libc6"}, install: "libc6"},
	"gcc-runtime":      {packages: []string{"libgcc-s1"}, install: "libgcc-s1"},
	"timezone-data":    {packages: []string{"tzdata"}, install: "tzdata"},
	"zlib-runtime":     {packages: []string{"zlib1g"}, install: "zlib1g"},
	"kerberos-runtime": {packages: []string{"libgssapi-krb5-2"}, install: "libgssapi-krb5-2"},
	// Debian/Ubuntu runtime package names vary across releases (including t64).
	// Development packages pull the current runtime when one is missing. The
	// supported .NET LTS distributions use OpenSSL 3.
	"icu-runtime": {pattern: `^libicu[0-9]+(?:t64)?$`, install: "libicu-dev"},
	"ssl-runtime": {pattern: `^libssl3(?:t64)?$`, install: "libssl-dev"},
	"terminfo":    {packages: []string{"ncurses-base"}, install: "ncurses-base"},
}

type debianPackageProvider struct {
	architecture string
	query        func(context.Context) (string, error)
	run          func(context.Context, ...string) error
}

func (p debianPackageProvider) Check(ctx context.Context, deps []SystemDependency) (SystemDependencyReport, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	report := SystemDependencyReport{Platform: "Debian/dpkg"}
	output, err := p.query(ctx)
	if err != nil {
		return report, fmt.Errorf("read dpkg package database: %w", err)
	}
	installed := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[2] != "installed" || (fields[1] != "all" && fields[1] != p.architecture) {
			continue
		}
		installed[strings.SplitN(fields[0], ":", 2)[0]] = true
	}
	for _, dep := range deps {
		rule, ok := debianRequirements[dep.Capability]
		if !ok {
			return report, fmt.Errorf("no Debian mapping for capability %s", dep.Capability)
		}
		found := false
		for _, name := range rule.packages {
			found = found || installed[name]
		}
		if len(rule.all) > 0 {
			complete := true
			for _, name := range rule.all {
				complete = complete && installed[name]
			}
			found = found || complete
		}
		if rule.pattern != "" {
			pattern := regexp.MustCompile(rule.pattern)
			for name := range installed {
				found = found || pattern.MatchString(name)
			}
		}
		if !found {
			report.Missing = append(report.Missing, MissingSystemDependency{dep.Capability, []string{rule.install}, dep.RequiredBy})
		}
	}
	if len(report.Missing) > 0 {
		report.InstallCommand = "sudo apt-get install --no-install-recommends " + strings.Join(report.packageNames(), " ")
	}
	return report, nil
}

func (p debianPackageProvider) Install(ctx context.Context, deps []SystemDependency) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("deps install requires root on Debian; run it as root during the image build")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	report, err := p.Check(ctx, deps)
	if err != nil || len(report.Missing) == 0 {
		return err
	}
	if p.run == nil {
		return fmt.Errorf("Debian package installer is unavailable")
	}
	if err := p.run(ctx, "/usr/bin/apt-get", "update"); err != nil {
		return fmt.Errorf("refresh apt package indexes: %w", err)
	}
	args := append([]string{"/usr/bin/apt-get", "install", "-y", "--no-install-recommends"}, report.packageNames()...)
	if err := p.run(ctx, args...); err != nil {
		return fmt.Errorf("install system prerequisites: %w", err)
	}
	verified, err := p.Check(ctx, deps)
	if err != nil {
		return err
	}
	if len(verified.Missing) > 0 {
		return &systemDependenciesError{verified}
	}
	return nil
}
