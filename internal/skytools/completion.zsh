# Managed by skytools. Tab completion for skytools and the tools it manages;
# env.sh sources this in interactive zsh.

# Completion needs compinit; most zsh configurations have already run it.
if ! (( $+functions[compdef] )); then
  autoload -Uz compinit && compinit -i
fi

_skytools_tools() {
  local -a tools
  local line
  for line in ${(f)"$(command $service list 2>/dev/null)"}; do
    tools+=("${line%% *}[${${${line#* }##[[:space:]]#}//[\[\]]/}]")
  done
  _values -s , tool $tools
}

_skytools() {
  local -a commands
  commands=(
    'ensure:install missing tools; keep what is installed'
    'update:ensure, then update to the latest LTS or stable'
    'self-update:update skytools itself from the latest release'
    'status:show the local inventory'
    'list:show the names accepted by --only'
    'version:show the skytools version'
    'deps:check the OS packages tools need'
    'setup:add the shell hook without installing tools'
    'env:print the shell environment'
    'ui:open the dashboard'
  )
  if (( CURRENT == 2 )); then
    _describe -t commands 'skytools command' commands
    return
  fi
  local only='--only=[comma-separated tool names]:tools:_skytools_tools'
  shift words
  (( CURRENT-- ))
  case $words[1] in
    ensure|update) _arguments -S $only ;;
    status) _arguments -S $only '--versions[also ask each tool for its version]' ;;
    deps) _arguments -S $only '1::action:(install)' ;;
  esac
}
compdef _skytools skytools sky-tools

# Managed tools' scripts: _name files are completion functions, autoloaded on
# first use for the commands on their #compdef line; name.zsh files register
# themselves when sourced.
() {
  emulate -L zsh
  local dir=$HOME/.local/share/skytools/completions/zsh file line
  [[ -d $dir ]] || return 0
  fpath=($dir ${fpath:#$dir})
  for file in $dir/_*(N); do
    IFS= read -r line < $file
    [[ $line == '#compdef '* ]] || continue
    (( $+functions[${file:t}] )) && unfunction ${file:t}
    autoload -Uz ${file:t}
    compdef ${file:t} ${=line#\#compdef }
  done
  for file in $dir/*.zsh(N); do
    source $file
  done
}
