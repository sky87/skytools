package skytools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// releaseBinary is a tool shipped as one executable in its official GitHub
// releases. asset and path are templates: {tag} is the release tag, {version}
// the tag without its prefix, and {arch} the project's name for the CPU.
type releaseBinary struct {
	repository string
	arch       map[string]string
	asset      string
	path       string // inside the archive; empty when the asset is the executable
	binary     string // installed as ~/.local/bin/<binary>
}

var (
	rustArch  = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}
	goArch    = map[string]string{"amd64": "amd64", "arm64": "arm64"}
	linuxArch = map[string]string{"amd64": "x86_64", "arm64": "arm64"}
)

// Prefer the static musl builds; delta and ast-grep publish glibc builds for
// both architectures only.
var releaseBinaries = map[string]releaseBinary{
	"btop":       {"aristocratos/btop", rustArch, "btop-{arch}-unknown-linux-musl.tar.gz", "btop/bin/btop", "btop"},
	"ripgrep":    {"BurntSushi/ripgrep", rustArch, "ripgrep-{version}-{arch}-unknown-linux-musl.tar.gz", "ripgrep-{version}-{arch}-unknown-linux-musl/rg", "rg"},
	"jq":         {"jqlang/jq", goArch, "jq-linux-{arch}", "", "jq"},
	"fd":         {"sharkdp/fd", rustArch, "fd-{tag}-{arch}-unknown-linux-musl.tar.gz", "fd-{tag}-{arch}-unknown-linux-musl/fd", "fd"},
	"shellcheck": {"koalaman/shellcheck", rustArch, "shellcheck-{tag}.linux.{arch}.tar.gz", "shellcheck-{tag}/shellcheck", "shellcheck"},
	"ast-grep":   {"ast-grep/ast-grep", rustArch, "app-{arch}-unknown-linux-gnu.zip", "ast-grep", "ast-grep"},
	"lazygit":    {"jesseduffield/lazygit", linuxArch, "lazygit_{version}_linux_{arch}.tar.gz", "lazygit", "lazygit"},
	"delta":      {"dandavison/delta", rustArch, "delta-{version}-{arch}-unknown-linux-gnu.tar.gz", "delta-{version}-{arch}-unknown-linux-gnu/delta", "delta"},
	"direnv":     {"direnv/direnv", goArch, "direnv.linux-{arch}", "", "direnv"},
	"just":       {"casey/just", rustArch, "just-{version}-{arch}-unknown-linux-musl.tar.gz", "just", "just"},
	"zoxide":     {"ajeetdsouza/zoxide", rustArch, "zoxide-{version}-{arch}-unknown-linux-musl.tar.gz", "zoxide", "zoxide"},
}

// binaryAliases are symlinks in ~/.local/bin to a tool's binary. ast-grep's
// own sg is a deprecated wrapper that warns on every run and needs ast-grep on
// PATH, so sg links to ast-grep instead. It takes precedence over the system's
// sg(1).
var binaryAliases = map[string][]string{"ast-grep": {"sg"}}

// installedBinaries are the names a release binary tool puts in ~/.local/bin.
func installedBinaries(name string) []string {
	return append([]string{releaseBinaries[name].binary}, binaryAliases[name]...)
}

const neovimRepository = "neovim/neovim"

// releaseRepositories lists every tool whose latest version is its latest
// stable GitHub release.
func releaseRepositories() map[string]string {
	repositories := map[string]string{"uv": "astral-sh/uv", "bun": "oven-sh/bun", "fzf": "junegunn/fzf", "gh": "cli/cli", "lean": "leanprover/lean4", "neovim": neovimRepository}
	for name, spec := range releaseBinaries {
		repositories[name] = spec.repository
	}
	return repositories
}

type verifiedAsset struct {
	tag, version, arch, url, checksum string
}

func (a verifiedAsset) expand(template string) string {
	return strings.NewReplacer("{tag}", a.tag, "{version}", a.version, "{arch}", a.arch).Replace(template)
}

// releaseAsset finds this machine's asset in the latest stable release, with the
// SHA-256 digest GitHub records for it. Nothing is downloaded without a digest.
func (m *manager) releaseAsset(name, repository string, arches map[string]string, template string) (verifiedAsset, error) {
	if runtime.GOOS != "linux" {
		return verifiedAsset{}, fmt.Errorf("%s binary installation currently supports Linux only", name)
	}
	a := verifiedAsset{arch: arches[runtime.GOARCH]}
	if a.arch == "" {
		return a, fmt.Errorf("unsupported %s architecture %s", name, runtime.GOARCH)
	}
	release, err := m.githubRelease(repository)
	if err != nil {
		return a, err
	}
	a.tag, a.version = release.Tag, release.Version
	asset := a.expand(template)
	if a.checksum, err = m.githubDigest(release, asset); err != nil {
		return a, err
	}
	a.url = release.DownloadURL(asset)
	return a, nil
}

func (m *manager) installReleaseBinary(name string) error {
	spec, ok := releaseBinaries[name]
	if !ok {
		return fmt.Errorf("no release binary for %s", name)
	}
	asset, err := m.releaseAsset(name, spec.repository, spec.arch, spec.asset)
	if err != nil {
		return err
	}
	var data []byte
	if spec.path == "" {
		data, err = m.verifiedDownload(asset.url, asset.checksum)
	} else {
		data, err = m.archiveMember(asset, name, asset.expand(spec.path))
	}
	if err != nil {
		return err
	}
	if err := atomicWrite(m.at(".local/bin", spec.binary), data, 0755); err != nil {
		return err
	}
	for _, alias := range binaryAliases[name] {
		if err := link(spec.binary, m.at(".local/bin", alias)); err != nil {
			return err
		}
	}
	return nil
}

func (m *manager) archiveMember(asset verifiedAsset, name, member string) ([]byte, error) {
	dir, err := m.archive(asset.url, asset.checksum, m.at(".local/state/skytools"))
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "contents", member)
	// Zip archives may not record the executable bit, so require a regular
	// file rather than an executable one; atomicWrite sets the mode.
	if info, err := os.Lstat(source); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unexpected %s archive layout: %s is missing", name, member)
	}
	return os.ReadFile(source)
}

// verifiedDownload is for assets that are the executable itself.
func (m *manager) verifiedDownload(url, checksum string) ([]byte, error) {
	if !validChecksum(checksum) {
		return nil, fmt.Errorf("invalid SHA-256 checksum")
	}
	var data bytes.Buffer
	if err := m.download(url, &data); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data.Bytes())
	if !strings.EqualFold(hex.EncodeToString(hash[:]), checksum) {
		return nil, fmt.Errorf("SHA-256 mismatch for %s", url)
	}
	return data.Bytes(), nil
}

// installNeovim keeps each release in ~/.neovim/releases/<version>, next to the
// runtime files nvim finds relative to itself, and points ~/.neovim/current at
// the active one, as Go and the JDK do.
func (m *manager) installNeovim() error {
	asset, err := m.releaseAsset("neovim", neovimRepository, linuxArch, "nvim-linux-{arch}.tar.gz")
	if err != nil {
		return err
	}
	if !releaseVersion.MatchString(asset.version) {
		return fmt.Errorf("invalid neovim release %s", asset.tag)
	}
	root := m.at(".neovim/releases")
	target := filepath.Join(root, asset.version)
	if !neovimTree(target) {
		dir, err := m.archive(asset.url, asset.checksum, root)
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		source := filepath.Join(dir, "contents", asset.expand("nvim-linux-{arch}"))
		if !neovimTree(source) {
			return fmt.Errorf("unexpected neovim archive layout")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := os.Rename(source, target); err != nil {
			return err
		}
	}
	return link(target, m.at(".neovim/current"))
}

func neovimTree(root string) bool {
	info, err := os.Stat(filepath.Join(root, "share/nvim/runtime"))
	return executable(filepath.Join(root, "bin/nvim")) && err == nil && info.IsDir()
}
