# skytools

Bring one executable to a new machine or container and set up developer tools
under your own home directory. No Go installation is needed to run it.
The application uses **only the Go standard library** and is built with CGO disabled.

This is the Go migration of `sky-tools` from
[`sky87/skydevcontainer`](https://github.com/sky87/skydevcontainer/blob/542474badc2f96613c8ab790f12e18f993af3fa1/sky-tools).
It retains `ensure`, `update`, the managed tool set, and their existing home paths.
The devcontainer now downloads this executable instead of bundling its Bash predecessor.

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
skytools self-update             # update skytools itself from GitHub's latest release
skytools status                  # fast local installed/missing/incomplete inventory
skytools status --versions       # explicitly run version commands
skytools deps --only rust,btop   # check system packages without installing anything
skytools deps install --only rust,btop # explicitly install prerequisites as root
skytools list                    # show names accepted by --only
skytools ensure --only uv,go,gh   # install a subset
skytools ensure --only pi        # also ensures Node.js and pnpm
skytools ensure --only playwright-cli # agent CLI, script libraries, browsers, Node.js
skytools setup                   # configure the shell without installing tools
eval "$(skytools env)"           # activate the environment in the current shell
```

### Dashboard

The dashboard goes through one sequence: look, pick, review, run, read the result.
There are no modes. A row's state decides what happens to it:

| Status | Meaning | Enter does |
| --- | --- | --- |
| `↑ update` | A newer LTS or stable version is known | Update |
| `✓ current` | Matches the target channel | Nothing; Space selects it for a forced update |
| `○ missing` | Not installed | Install |
| `! incomplete` | Partly installed | Repair |
| `? check failed`, `? unknown` | Version check failed or versions cannot be compared; never shown as an update | Update |
| `» ahead` | Newer than the target channel | Nothing; Space selects it to install the target |

`· system` marks a tool found on PATH. `ensure` keeps it, and Update installs a
managed copy under your home directory without touching the system one.

Review lists each package's action and everything else a run will do before
anything changes: dependencies, system prerequisites, and the shell hook. A
dependency you did not select stays as it is if already installed, so updating
`pi` leaves `node` alone. `skytools update --only pi` updates both. Missing system
packages stop the plan here, with the full apt command on screen; `x` drops the
blocked packages and `q` quits and prints the command. skytools never runs it.

Run shows each package's progress and a scrollable log that keeps every line, in
memory only. Result separates what finished, failed, and did not start, because a
run stops at its first failure. After you quit, a short receipt stays on the normal
screen with what changed, any failure, and the commands to run next. Browsing
without changing anything prints nothing.

| Key | Action |
| --- | --- |
| `↑` `↓` `j` `k`, `PgUp` `PgDn`, `g` `G` | Move, page, first and last |
| `Tab` `Shift-Tab` | Next or previous view: All, Updates, Missing, Issues |
| `/` | Filter by name |
| `Space` | Select or clear this row |
| `u` `m` `a` | Select all updates, all missing, or everything in the view |
| `Enter` | Review the selection, or this row |
| `→` | Details of this row, with errors in full |
| `r` | Rescan files and check versions again |
| `p` | System prerequisites for all packages |
| `S` | Update skytools itself |
| `l` | Log of the last run |
| `Esc` | Back, clear the filter, clear the selection |
| `Ctrl-C` | Cancel a run at once, or quit |
| `q` | Quit; asks first during a run |
| `?` | All of the above |

Only basic ANSI colors are used, so the dashboard follows your terminal theme, and
every state also has a symbol and a word. `NO_COLOR` keeps bold and reverse and
drops color. If `LC_ALL`, `LC_CTYPE`, and `LANG` do not name UTF-8, symbols become
ASCII of the same width. The list adds details beside it from 110 columns and needs
at least 36 x 10. Without an interactive terminal, running `skytools` prints the
local inventory and exits without changing anything.

On platforms without a prerequisite provider, currently anything but Debian-family
Linux, the dashboard says so on open and allows browsing and self-update only.

The first dashboard frame uses executable paths, symlinks, and native toolchain
metadata. This local detection performs no execution or network requests. Once
the screen is visible, four background workers fill in missing installed versions
with bounded version commands and fetch current upstream releases, plus the latest
skytools release. Checks are cancellable; offline failures and unknown comparisons
never produce update alerts. Finishing a run re-reads only the packages it touched.
There is no installation manifest or persistent version cache. `status` and
redirected output remain local and filesystem-only; `status --versions` explicitly
queries executables. Complete system/PATH installations are recognized too.

`ensure` and `update` print numbered steps with installer output indented, then a
summary naming what did not start. Upstream installers run with no controlling
terminal, so one that prompts takes its noninteractive path and cannot stall a run.

`ensure` and `update` serialize changes with a per-home lock. A failed tool
installation stops the command with an error; rerun it after fixing the cause.
`self-update` selects the latest stable release of `sky87/skytools`, downloads the
binary for the running OS/architecture, and verifies its published SHA-256 checksum.
It validates the candidate's version before replacing the executable atomically;
failed downloads or checks leave the current binary intact. Symlinks remain intact,
permissions are preserved, and equal/newer versions are kept. The next invocation
uses the new binary. The executable's directory must be writable. For an image-owned
`/usr/local/bin/skytools`, rebuild the container image or use a user-owned copy.
This command changes only skytools; it does not update managed tools or shell files.
Release archives for Go, Corretto, GitHub CLI, btop, and Tailscale are checked against upstream
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
| `tailscale` | Tailscale client and daemon, latest stable Linux binaries | `~/.local/bin/tailscale`, `~/.local/bin/tailscaled` |
| `playwright` | Playwright and playwright-core Node libraries, Chromium, Firefox, WebKit | `~/.local/share/skytools/playwright`, `~/.cache/ms-playwright` |
| `playwright-cli` | Microsoft's browser automation CLI for coding agents | same shared prefix; `~/.local/bin/playwright-cli` |

skytools only installs the Tailscale binaries. It never starts `tailscaled`, sets
up a service, or logs you in, because the daemon normally needs root. To run it
without root, use userspace networking, then log in through the same socket:

```sh
tailscaled --tun=userspace-networking \
  --socket="$HOME/.local/state/tailscale/tailscaled.sock" \
  --statedir="$HOME/.local/state/tailscale" &
tailscale --socket="$HOME/.local/state/tailscale/tailscaled.sock" up
```

Userspace networking has no TUN device, so other programs reach the tailnet only
through Tailscale's SOCKS5 or HTTP proxy options. A `tailscale` client already on
PATH counts as installed; `tailscaled` is not checked there because system packages
put it in `/usr/sbin`. Tailscale installation is Linux only.

Python versions can be installed with `uv python install` after setup. Operating
system packages from the old devcontainer (editors, C/C++ compilers, LaTeX,
document tools, native libraries) remain OS prerequisites rather than managed
home installations. skytools never invokes `sudo`. Only the explicit
`deps install` command invokes the platform's system package manager.

## Browser automation

`playwright-cli` depends on `playwright`, which depends on `node`. Selecting the
CLI installs all three; selecting just `playwright` installs the script libraries
and browsers. Both packages follow npm's latest stable release rather than a
fixed version. Playwright has no LTS channel. Each package installs the browser
revisions required by its own driver, which can differ between the CLI and library.

```sh
# On the target machine, as root:
skytools deps install --only playwright-cli
# As the target user:
skytools ensure --only playwright-cli
eval "$(skytools env)"
playwright-cli open https://example.com
playwright-cli snapshot
playwright-cli close-all
playwright-node script.mjs
playwright-node script.cjs
```

Use `playwright-node` to run standalone Node scripts that import `playwright` or
`playwright-core`, with either ES modules or CommonJS. It falls back to the shared
home installation when the script has no local copy; project-local packages take
precedence. For example, `script.mjs` can contain:

```js
import { chromium } from 'playwright';
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage();
await page.goto('https://example.com');
console.log(await page.title());
await browser.close();
```

The agent CLI defaults to its downloaded Chromium through
`~/.playwright/cli.config.json`, created only when absent. Existing user and project
configuration keeps the upstream precedence. skytools does not edit project
dependencies or install agent skills. The libraries live under
`~/.local/share/skytools/playwright/lib/node_modules`; command links are in
`~/.local/bin`. Browser downloads use Playwright's native cache and respect
`PLAYWRIGHT_BROWSERS_PATH`. Inventory reads package and browser metadata without
starting Node or a browser, and reports incomplete downloads as needing repair.

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
command. `ensure`/`update` never run it or escalate privileges. Run
`skytools deps` (optionally with `--only`) to inspect requirements without changing
your home; a nonzero exit means packages are missing or checks failed.

Use `skytools deps install` as root to install prerequisites for all packages, or
pass `--only` for a subset. The dependency graph lives in code: tool dependencies
are resolved transitively before collecting system requirements. The Debian
installer checks installed packages, refreshes apt indexes once when needed,
installs a deduplicated list of missing packages, and verifies the result. It does
not install user toolchains, modify shell files, or keep an installation manifest.
The plain `deps` command remains read-only. Explicit `--only all` is supported.

The model uses generic capabilities such as an HTTP client, C build tools, and
SSL runtime. A platform provider maps these to package names. The initial provider
supports Debian/Ubuntu and derivatives identified by `/etc/os-release`. It queries
dpkg once, accepting only configured packages for the native architecture or
architecture-independent packages. Other platforms report that prerequisite
checks are unsupported before installation begins.

Installer prerequisites include Bash, CA certificates, curl, git, tar/gzip, unzip,
and xz, depending on the tool. Rust/OCaml/Lean also need native build tools.
.NET needs ICU, OpenSSL 3, the C/C++ and Kerberos runtimes, timezone data, and zlib.
OCaml needs m4, pkg-config, rsync, GMP development files, and bubblewrap (unless opam
sandboxing is explicitly disabled as described above). btop needs terminal data
(`ncurses-base`). Existing complete tools skip installer-only prerequisites on
`ensure`, while runtime prerequisites are still checked. For missing ICU/OpenSSL,
the suggested development packages pull in the distribution's runtime package.
Playwright additionally needs its browser libraries, fonts, and Xvfb. Its Debian
provider uses the upstream browser dependency tables for Debian 12/13 and Ubuntu
22.04/24.04/26.04 on amd64/arm64, accepting configured virtual package providers
such as the t64 replacements. Unknown browser platforms fail explicitly. Source
attribution and the upstream license are in
[`playwrightdata`](internal/skytools/playwrightdata/).

The `runtime` Docker stage contains basic installer utilities and .NET runtime
libraries, with no managed language toolchains preinstalled. The `full` stage
uses `skytools deps install` to add every managed tool's system prerequisites,
including OCaml build tools and Playwright browser libraries. Neither includes
the old devcontainer's editors, profiling tools, or document stack.

## Development: Docker only

**Do not run Go, tests, skytools, or installers directly on the development host.**
Git, `gh`, Docker, and `make` are orchestration tools; every project build,
format operation, test, and application execution below happens inside Docker.

```sh
make fmt               # gofmt inside Docker
make test              # enforce stdlib-only modules, formatting, go vet, unit tests
make smoke             # above + static executable in a minimal non-root container
make integration       # install uv/Go/gh/btop, repeat ensure offline in the same test home
make playwright-integration # install agent CLI/libraries; launch all browsers offline
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
Go container. CI runs the minimal-container checks, real subset installation,
and Playwright CLI/CommonJS/ES module checks with offline Chromium, Firefox,
and WebKit on Linux amd64 and arm64. The browser checks also exercise cached
repair and project-local module precedence.

## Releases

Pushing a `v*` tag runs Docker checks, cross-compiles Linux/macOS executables for
amd64/arm64, and publishes them with `SHA256SUMS` to a public GitHub release:

```sh
git tag v0.1.0
git push origin v0.1.0
```

No source checkout or Go compiler is required on the target machine.
