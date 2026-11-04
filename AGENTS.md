# skytools

## Goal and architecture

Create a portable tool that the user can bring to a new machine or container,
download as one executable, and use to set up and update developer tools in the
user's home directory. This repository migrates `../skydevcontainer/sky-tools`
from Bash to Go. Keep that source repository unchanged unless asked otherwise.

- **Go standard library only.** Do not add external Go modules, frameworks,
  libraries, code generators, or testing dependencies. Keep `go.mod` dependency
  free. Use `CGO_ENABLED=0` for distributable executables.
- `cmd/skytools/main.go`: CLI entry point and build version.
- `internal/skytools`: CLI, tool registry, processes, downloads, archive/checksum
  handling, locking, filesystem inventory, dashboard, and embedded shell environment.
- `internal/termui`: original immediate-mode terminal controls, raw input,
  sanitized canvas rendering, and terminal restoration. No external TUI library.
- `Dockerfile`: Go build/test stages, a minimal non-root runtime, and a separate
  stage with OCaml build prerequisites.
- `scripts/`: container smoke checks and release cross-compilation.
- `.github/workflows/`: Docker CI and tagged binary releases.

Preserve the `ensure` (no updates), `update` (explicit updates), and `status`
commands and existing home locations when changing the manager. Tools must run
as the target user; never invoke sudo or install OS packages from the application.
Explain OS prerequisites in README.md. Avoid overwriting user dotfiles and keep
shell configuration idempotent. Verify upstream SHA-256 checksums where available;
stage archive installs before changing active pointers. Keep Linux amd64/arm64
working; cross-compile macOS amd64/arm64 without running anything on this host.
`self-update` updates only the running skytools executable from the latest stable
GitHub release. Verify the matching asset's SHA-256 and reported version before an
atomic replacement, preserve symlinks and permissions, and retain the old binary
on failure or cancellation. Never escalate privileges for image-owned executables.

Running without a command opens the dashboard when stdin and stdout are terminals;
otherwise print a read-only inventory. Keep package detection fast and offline:
read executable paths, symlinks, and native distribution metadata, without
launching package binaries or maintaining an installed-package manifest. Show
missing and incomplete toolchains accurately, including manager proxies without
their runtimes. The dashboard must present that local inventory first, then run
bounded, cancellable background probes for installed versions missing from native
metadata and current upstream LTS/stable versions. Highlight known updates only;
show errors or unknown comparisons honestly. Keep `status` and noninteractive
inventory filesystem-only. Do not persist version results or installation records.

Resolve the latest upstream LTS for Node.js, .NET, Java, and OCaml dynamically during
Install/Update. Tools without an official LTS use their latest stable channel.
Use the same policies in dashboard release checks and installation paths.
Never freeze runtime majors or helper versions in the source. Keep complete
existing installations on `ensure`, including system/PATH tools; an explicit
Update can create a managed home copy. Derive installed Java information from
the JDK's own `release` file rather than skytools marker files.

Declare UI controls every frame with stable IDs and keep application state
outside widget instances. Restore terminal modes and cursor state on exit and
cancellation. Run jobs in the background, bound their displayed logs, sanitize
control sequences, and rescan actual files when jobs finish.

Model system prerequisites as generic capabilities with platform providers.
Only the Debian-family provider is implemented initially. Install/Update must
check all selected tools' prerequisites before starting installers, accepting only
installed native/all-architecture dpkg packages. Report missing packages and an
apt command; never run it automatically. Keep `deps` read-only. Include automatic
tool dependencies such as Node for pi. btop uses its checksummed official Linux
release binary in `~/.local/bin/btop`.

## Development and validation

**Everything related to Go or the application runs in Docker.** Never execute
`go`, `gofmt`, tests, a built skytools executable, an installer, or a managed tool
on the development host. Do not install development dependencies on this machine.
Host Git, gh, Docker, make, and file editing are permitted as orchestration.

- `make fmt`: format using Go inside Docker.
- `make test`: stdlib-only dependency assertion, gofmt check, go vet, unit tests.
- `make smoke`: also run the standalone executable in a minimal non-root container.
- `make integration`: real uv/Go/gh/btop installation, then offline ensure and status.
- `make full-integration`: complete tool installation with native prerequisites.
- `make build`: export a Linux binary to `dist/`.
- `make release VERSION=vX.Y.Z`: cross-compile four release binaries and checksums.

Use disposable Docker homes/volumes. Never mount the user's real home into an
installation test. Run checks relevant to each task; defer expensive complete
installations until the whole change is ready. Keep README.md accurate about
what was tested, platform support, tool locations, and prerequisites.

## Agent workspace and investigations

`ai/` is gitignored and must never be versioned. It is for communication with the
user; use `ai/tmp` for scratch files. Do not create worktrees unless the user
asks; if requested, put them under `ai/worktree`.

Investigations and troubleshooting are read-only unless the user explicitly
authorizes execution or changes. Verify statements in source code or read-only
database queries. Explain the problem, any ask, and the recommendation in plain,
concise language for an engineer unfamiliar with the codebase. Analyze multiple
issues separately.

## Plans

When asked for a plan, write `ai/plan-<name>.md`. Verify existing behavior from
source or read-only queries; support statements with file references and snippets.
Check established approaches and explain any proposed divergence. Provide the
context an engineer new to this repository needs. After drafting, ask design
questions one at a time in normal conversation, with context and concrete options.
Revise the plan once all questions have been answered, not after each answer.
Finish with a concise breakdown and links to relevant plan sections, including
any controversial decisions.

Repository: https://github.com/sky87/skytools.

Keep personal workflow instructions, private issue-tracker links, and credential
locations outside the public repository. The ignored `ai/` directory is available
for local notes. Never commit credentials or machine-specific configuration.

## Commits

Commit after each completed task, after relevant checks. Use conventional subjects
such as `feat: ...` or `fix (ISSUE-123): ...`, followed by a blank line and a
substantial body explaining the change, rationale, validation, and follow-up.
Use the Linear key for issue-related commits. Write exact multiline messages via:

```sh
git commit -F - <<'EOF'
feat: short description

Explain the changes and why they were needed, including relevant validation.
EOF
```
