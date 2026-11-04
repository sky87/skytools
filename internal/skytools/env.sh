# Managed by skytools. Safe to source from sh, bash, and zsh.
export NVM_DIR="$HOME/.nvm"
export RUSTUP_HOME="$HOME/.rustup"
export CARGO_HOME="$HOME/.cargo"
export BUN_INSTALL="$HOME/.bun"
export DOTNET_ROOT="$HOME/.dotnet"
export GOROOT="$HOME/.go/current"
export GOPATH="$HOME/go"
export JAVA_HOME="$HOME/.jdk/current"
export FZF_HOME="$HOME/.fzf"
export OPAMROOT="$HOME/.opam"
export ELAN_HOME="$HOME/.elan"
export PNPM_HOME="$HOME/.local/share/pnpm"
export DOTNET_CLI_TELEMETRY_OPTOUT=1

# Prepend missing entries without growing PATH every time the file is sourced.
for _skytools_dir in "$ELAN_HOME/bin" "$OPAMROOT/default/bin" "$FZF_HOME/bin" \
  "$JAVA_HOME/bin" "$GOPATH/bin" "$GOROOT/bin" "$DOTNET_ROOT" "$BUN_INSTALL/bin" \
  "$CARGO_HOME/bin" "$NVM_DIR/current/bin" "$PNPM_HOME" "$PNPM_HOME/bin" \
  "$HOME/.opencode/bin" "$HOME/.local/bin"; do
  case ":${PATH:-}:" in
    *":$_skytools_dir:"*) ;;
    *) PATH="$_skytools_dir${PATH:+:$PATH}" ;;
  esac
done
unset _skytools_dir
export PATH

# Shell hooks are only needed in interactive shells.
case $- in
  *i*)
    if [ -n "${BASH_VERSION:-}${ZSH_VERSION:-}" ] && [ -s "$NVM_DIR/nvm.sh" ]; then
      . "$NVM_DIR/nvm.sh"
    fi
    if [ -s "$OPAMROOT/opam-init/init.sh" ]; then
      . "$OPAMROOT/opam-init/init.sh" >/dev/null 2>&1
    fi
    ;;
esac
