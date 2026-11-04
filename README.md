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
from [GitHub Releases](https://github.com/sky87/skytools/releases). Release assets
are public and can be downloaded without a GitHub account or access token.

| Platform | Asset |
| --- | --- |
| Linux x86-64 (glibc) | `skytools_linux_amd64` |
| Linux ARM64 (glibc) | `skytools_linux_arm64` |
| macOS Intel | `skytools_darwin_amd64` |
| macOS Apple Silicon | `skytools_darwin_arm64` |

For example, on a target Linux ARM64 machine:

```sh
release=https://github.com/sky87/skytools/releases/latest/download
curl --fail --location "$release/skytools_linux_arm64" -o skytools_linux_arm64
curl --fail --location "$release/SHA256SUMS" -o SHA256SUMS
grep '  skytools_linux_arm64$' SHA256SUMS | sha256sum --check -
mkdir -p "$HOME/.local/bin"
install -m 0755 skytools_linux_arm64 "$HOME/.local/bin/skytools"
"$HOME/.local/bin/skytools"
eval "$("$HOME/.local/bin/skytools" env)"
```

On macOS, verify the matching asset with `shasum -a 256 --check` instead of
`sha256sum --check`. macOS builds are cross-compiled in Docker and support the
dashboard, inventory, and shell setup. Install/Update prerequisite checks currently
support Debian-based Linux only. Windows and Alpine/musl tool installations are
not currently supported.

## Commands

```sh
skytools                         # open the interactive package dashboard
skytools ensure                  # install missing tools; leave installed versions alone
skytools update                  # ensure tools exist, then explicitly update them
skytools status                  # fast local installed/missing/incomplete inventory
skytools status --versions       # explicitly run version commands
skytools deps --only rust,btop   # check system packages without installing anything
skytools list                    # show names accepted by --only
skytools ensure --only uv,go,gh   # install a subset
skytools ensure --only pi        # also ensures Node.js and pnpm
skytools setup                   # configure the shell without installing tools
eval "$(skytools env)"           # activate the environment in the current shell
```

The dashboard shows installed versions alongside the latest upstream LTS (or
stable version for tools without LTS), with available updates highlighted in cyan.
It distinguishes missing and incomplete packages. Select an
Install or Update action, choose packages, then run the selection. Installation
output stays visible while the keyboard remains responsive, and cancelling an
operation returns to the dashboard. Without an interactive terminal, running
`skytools` prints the local inventory and exits without changing anything.

Use the arrow keys, Tab, or Shift-Tab to move focus. Space or Enter toggles an
eligible package; installed rows remain focusable to inspect their version and
path. `i` selects Install missing, `u` selects Update, `a` selects or clears all
eligible packages, and `r` refreshes inventory and upstream versions. Focus Run selected and
press Enter to start. Ctrl-C cancels a running action and returns to the
dashboard, or quits when idle. `q` quits after cancelling any active action.

The first dashboard frame uses executable paths, symlinks, and native toolchain
metadata. This local detection performs no execution or network requests. Once
the screen is visible, four background workers fill in missing installed versions
with bounded version commands and fetch current upstream releases. Checks are
cancellable; offline failures and unknown comparisons never produce update alerts.
Focus a row for full versions and error details. There is no installation manifest
or persistent version cache. `status` and redirected output remain local and
filesystem-only; `status --versions` explicitly queries executables.
Complete system/PATH installations are recognized too. `ensure` keeps them;
Update installs a managed copy under your home directory.

`ensure` and `update` serialize changes with a per-home lock. A failed tool
installation stops the command with an error; rerun it after fixing the cause.
Release archives for Go, Corretto, GitHub CLI, and btop are checked against upstream
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
| `jdk` | Amazon Corretto, latest Java LTS | `~/.jdk/releases`, `~/.jdk/current` |
| `fzf` | fzf | `~/.fzf` |
| `gh` | GitHub CLI | `~/.local/bin/gh` |
| `ocaml` | opam, latest OCaml LTS compiler | `~/.local/bin/opam`, `~/.opam` |
| `lean` | elan, Lean 4 stable | `~/.elan` |
| `claude` | Claude Code | `~/.local/bin/claude` |
| `codex` | Codex | `~/.local/bin/codex` |
| `opencode` | OpenCode | `~/.opencode/bin` |
| `pi` | pi coding agent | `~/.local/share/pnpm` |
| `btop` | btop resource monitor, latest stable Linux binary | `~/.local/bin/btop` |

Python versions can be installed with `uv python install` after setup. Operating
system packages from the old devcontainer (editors, C/C++ compilers, LaTeX,
document tools, native libraries) remain OS prerequisites rather than managed
home installations. skytools never invokes `sudo` or a system package manager.

## Release selection

Install and Update resolve the current upstream release when needed. No nvm
version or Java major is fixed in the application. The old
`SKY_TOOLS_NVM_VERSION` and `SKY_TOOLS_CORRETTO_VERSION` pins are no longer used.

| Package | Selection policy |
| --- | --- |
| Node.js | Latest LTS via nvm's upstream LTS channel |
| .NET | Latest LTS SDK via the official installer's LTS channel |
| Java | Latest LTS identified by official Adoptium release metadata, then the corresponding checksummed Amazon Corretto archive |
| OCaml | Latest release advertised as LTS by the official OCaml release metadata |
| nvm and fzf | Latest published stable GitHub release |
| Other tools | Latest upstream stable release/channel; these tools have no separate LTS selection |

The [Adoptium available-releases API](https://api.adoptium.net/v3/info/available_releases)
provides Java's current LTS major. The installed JDK remains Amazon Corretto;
installation fails clearly if Corretto does not yet publish that LTS archive.
Upstream native metadata, rather than skytools marker files, identifies the
installed JDK. Claude Code uses its documented
[stable channel](https://code.claude.com/docs/en/setup).

OCaml's LTS classification comes from the same
[official release metadata](https://github.com/ocaml/ocaml.org/tree/main/data/releases)
used by ocaml.org, following its
[compiler release cycle](https://ocaml.org/tools/compiler-release-cycle).

An LTS line can be older than the newest feature release. For example, selecting
Update for an OCaml installation on a newer feature line switches it to the
advertised LTS line. `ensure` leaves complete existing installations alone.

Set `SKY_TOOLS_DISABLE_OPAM_SANDBOX=1` to explicitly disable opam sandboxing when
Docker provides isolation and unprivileged namespaces are unavailable. Ordinary
target machines retain opam sandboxing.

Lean's default points to a concrete installed stable version so it works offline.
`update` explicitly resolves the stable channel and selects the newest installed
stable Lean version; `ensure` uses the newest stable version already installed.

The operation lock lives in `~/.local/state/skytools`. Run as the target user so
tools are installed into that user's home.

## Target prerequisites

The skytools executable itself needs no language runtime or shared Go libraries.
Before Install/Update starts any installer, skytools checks the selected tools'
system packages, including automatically selected tool dependencies such as Node
for pi. Missing packages stop the operation and produce an `apt-get install`
command. skytools never runs that command or escalates privileges. Run
`skytools deps` (optionally with `--only`) to inspect requirements without changing
your home; a nonzero exit means packages are missing or checks failed.

The model uses generic capabilities such as an HTTP client, C build tools, and
SSL runtime. A platform provider maps these to package names. The initial provider
supports Debian/Ubuntu and derivatives identified by `/etc/os-release`. It queries
dpkg once, accepting only configured packages for the native architecture or
architecture-independent packages. Other platforms report that prerequisite
checks are unsupported before installation begins.

Installer prerequisites include Bash, CA certificates, curl, git, tar/gzip, unzip,
and xz, depending on the tool. Rust/OCaml/Lean also need native build tools.
.NET needs ICU, OpenSSL 3, the C/C++ and Kerberos runtimes, timezone data, and zlib.
OCaml needs m4, pkg-config, GMP development files, and bubblewrap (unless opam
sandboxing is explicitly disabled as described above). btop needs terminal data
(`ncurses-base`). Existing complete tools skip installer-only prerequisites on
`ensure`, while runtime prerequisites are still checked. For missing ICU/OpenSSL,
the suggested development packages pull in the distribution's runtime package.

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
make integration       # install uv/Go/gh/btop, repeat ensure offline in the same test home
make full-integration  # install the complete tool set in an isolated Docker volume
make build             # native Linux executable exported to dist/skytools
make release VERSION=v0.1.0  # four executables + SHA256SUMS in dist/
```

Integration tests use disposable Docker volumes, never your real home. Test
fixtures run against local HTTP servers; they cover checksum failures, unsafe
archives, preserving the active Go release after failure, shell setup, command
validation, and lock contention. Terminal tests use Linux pseudo-terminals for
raw input, resize, cancellation, rendering, and restoration. Dashboard tests
also exercise actual keyboard-driven installation and cancellation using local
installer fixtures. Inventory benchmarks and sentinel executables check that
detection stays filesystem-only. Version tests cover LTS selection, update
comparisons, bounded workers, offline checks, stale refresh results, and actual
terminal responsiveness/restoration. Dependency fixtures cover missing,
unconfigured, and foreign-architecture packages and prevent installer execution
after a failed preflight. `GOPROXY=off` and `GOSUMDB=off` are set in the
Go container. CI runs the minimal-container checks and real subset installation
on Linux amd64 and arm64.

## Releases

Pushing a `v*` tag runs Docker checks, cross-compiles Linux/macOS executables for
amd64/arm64, and publishes them with `SHA256SUMS` to a public GitHub release:

```sh
git tag v0.1.0
git push origin v0.1.0
```

No source checkout or Go compiler is required on the target machine.
