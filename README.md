# skytools

Bring one executable to a new machine or container and set up developer tools
under your own home directory. No Go installation is needed to run it.
The application uses **only the Go standard library** and is built with CGO disabled.

This is the Go migration of `sky-tools` from
[`sky87/skydevcontainer`](https://github.com/sky87/skydevcontainer/blob/542474badc2f96613c8ab790f12e18f993af3fa1/sky-tools).
It retains `ensure`, `update`, the managed tool set, and their existing home paths.
The original repository is unchanged.

## Install on a target machine

Download the executable for your OS and architecture, along with `SHA256SUMS`,
from [GitHub Releases](https://github.com/sky87/skytools/releases). This repository
is private: sign in to GitHub to download assets, or use an already authenticated
`gh` CLI. No anonymous `curl` download is available for private release assets.

| Platform | Asset |
| --- | --- |
| Linux x86-64 (glibc) | `skytools_linux_amd64` |
| Linux ARM64 (glibc) | `skytools_linux_arm64` |
| macOS Intel | `skytools_darwin_amd64` |
| macOS Apple Silicon | `skytools_darwin_arm64` |

For example, on a target Linux ARM64 machine with `gh` already available:

```sh
gh release download --repo sky87/skytools \
  --pattern skytools_linux_arm64 --pattern SHA256SUMS
grep '  skytools_linux_arm64$' SHA256SUMS | sha256sum --check -
mkdir -p "$HOME/.local/bin"
install -m 0755 skytools_linux_arm64 "$HOME/.local/bin/skytools"
"$HOME/.local/bin/skytools" ensure
eval "$("$HOME/.local/bin/skytools" env)"
```

On macOS, verify the matching asset with `shasum -a 256 --check` instead of
`sha256sum --check`. macOS builds are cross-compiled in Docker; native macOS
installer execution is not covered by the Linux container tests. Windows and
Alpine/musl tool installations are not currently supported.

## Commands

```sh
skytools ensure                  # install missing tools; leave installed versions alone
skytools update                  # ensure tools exist, then explicitly update them
skytools status                  # report installed versions
skytools list                    # show names accepted by --only
skytools ensure --only uv,go,gh   # install a subset
skytools ensure --only pi        # also ensures Node.js and pnpm
skytools setup                   # configure the shell without installing tools
eval "$(skytools env)"           # activate the environment in the current shell
```

`ensure` and `update` serialize changes with a per-home lock. A failed tool
installation stops the command with an error; rerun it after fixing the cause.
Release archives for Go, Corretto, and GitHub CLI are checked against upstream
SHA-256 checksums, extracted in staging directories, and activated after validation.
Other tools use their upstream installers or package managers over HTTPS.

`ensure` also creates `~/.config/skytools/env.sh` and adds a source hook to
`~/.profile`, `~/.bashrc`, and `~/.zshrc`. If Bash already uses `.bash_profile` or
`.bash_login`, that file gets the hook too. Existing settings are retained and
backed up once as `<filename>.skytools.bak`; reruns do not duplicate the hook.
Fish and PowerShell shell integration are not implemented. Profiles symlinked
outside the home directory require adding the source hook manually.

The Docker image also provides `sky-tools` as an alias for compatibility with the
old name. When downloading the binary yourself, an optional equivalent is
`ln -s skytools "$HOME/.local/bin/sky-tools"`.

## Managed tools

| `--only` name | Tools | Home location |
| --- | --- | --- |
| `uv` | uv, for managing Python | `~/.local/bin` |
| `rust` | rustup, Rust, Cargo | `~/.rustup`, `~/.cargo` |
| `node` | nvm, Node.js LTS, npm, pnpm | `~/.nvm`, links in `~/.local/bin` |
| `bun` | Bun | `~/.bun` |
| `dotnet` | .NET LTS SDK | `~/.dotnet` |
| `go` | latest stable Go | `~/.go/releases`, `~/.go/current`; workspace `~/go` |
| `jdk` | Amazon Corretto 25 | `~/.jdk/releases`, `~/.jdk/current` |
| `fzf` | fzf | `~/.fzf` |
| `gh` | GitHub CLI | `~/.local/bin/gh` |
| `ocaml` | opam, latest stable OCaml compiler | `~/.local/bin/opam`, `~/.opam` |
| `lean` | elan, Lean 4 stable | `~/.elan` |
| `claude` | Claude Code | `~/.local/bin/claude` |
| `codex` | Codex | `~/.local/bin/codex` |
| `opencode` | OpenCode | `~/.opencode/bin` |
| `pi` | pi coding agent | `~/.local/share/pnpm` |

Python versions can be installed with `uv python install` after setup. Operating
system packages from the old devcontainer (editors, C/C++ compilers, LaTeX,
document tools, native libraries) remain OS prerequisites rather than managed
home installations. skytools never invokes `sudo` or a system package manager.

Configuration inherited from the old manager:

- `SKY_TOOLS_NVM_VERSION`: nvm installer version (default `v0.40.1`).
- `SKY_TOOLS_CORRETTO_VERSION`: Corretto major version (default `25`).
- `SKY_TOOLS_DISABLE_OPAM_SANDBOX=1`: explicitly disable opam sandboxing when
  Docker provides isolation and unprivileged namespaces are unavailable. Unlike
  the old container-only script, ordinary target machines retain opam sandboxing.

Lean's default points to a concrete installed stable version so it works offline.
`update` explicitly resolves the stable channel and selects the newest installed
stable Lean version; `ensure` uses the newest stable version already installed.

State and locking live in `~/.local/state/skytools`. Run as the target user so
tools are installed into that user's home.

## Target prerequisites

The skytools executable itself needs no language runtime or shared Go libraries.
Upstream installers need a POSIX environment with Bash, CA certificates, curl,
git, tar/gzip, unzip, and xz. .NET also needs its platform's ICU/OpenSSL/runtime
libraries. OCaml builds from source and needs a C/C++ toolchain, make, m4,
pkg-config, and GMP development files. macOS needs the Command Line Tools for
compiling OCaml and other native packages.

The `runtime` Docker stage contains basic installer utilities and .NET runtime
libraries, with no managed language toolchains preinstalled. The `full` stage
adds only OCaml's build prerequisites. Neither includes the old devcontainer's
editors, desktop libraries, profiling tools, or document stack.

## Development: Docker only

**Do not run Go, tests, skytools, or installers directly on the development host.**
Git, `gh`, Docker, and `make` are orchestration tools; every project build,
format operation, test, and application execution below happens inside Docker.

```sh
make fmt               # gofmt inside Docker
make test              # enforce stdlib-only modules, formatting, go vet, unit tests
make smoke             # above + static executable in a minimal non-root container
make integration       # install uv/Go/gh, repeat ensure offline in the same test home
make full-integration  # install the complete tool set in an isolated Docker volume
make build             # native Linux executable exported to dist/skytools
make release VERSION=v0.1.0  # four executables + SHA256SUMS in dist/
```

Integration tests use disposable Docker volumes, never your real home. Test
fixtures run against local HTTP servers; they cover checksum failures, unsafe
archives, preserving the active Go release after failure, shell setup, command
validation, and lock contention. `GOPROXY=off` and `GOSUMDB=off` are set in the
Go container. CI runs the minimal-container checks and real subset installation
on Linux amd64 and arm64.

## Releases

Pushing a `v*` tag runs Docker checks, cross-compiles Linux/macOS executables for
amd64/arm64, and publishes them with `SHA256SUMS` to a private GitHub release:

```sh
git tag v0.1.0
git push origin v0.1.0
```

No source checkout or Go compiler is required on the target machine.
