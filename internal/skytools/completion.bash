# Managed by skytools. Tab completion for skytools and the tools it manages;
# env.sh sources this in interactive bash.

_skytools() {
  local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=($(compgen -W 'ensure update self-update status list version deps setup env ui help' -- "$cur"))
    return
  fi
  local command=${COMP_WORDS[1]}
  # Readline splits --only=list into --only, = and list.
  if [ "$prev" = --only ] || { [ "$prev" = = ] && [ "${COMP_WORDS[COMP_CWORD-2]}" = --only ]; }; then
    case $command in ensure | update | status | deps) ;; *) return ;; esac
    local names= name rest done=
    while read -r name rest; do names="$names $name"; done < <("${COMP_WORDS[0]}" list 2>/dev/null)
    case $cur in *,*) done=${cur%,*}, ;; esac
    COMPREPLY=($(compgen -P "$done" -W "$names" -- "${cur##*,}"))
    return
  fi
  case $command in
    ensure | update) COMPREPLY=($(compgen -W '--only' -- "$cur")) ;;
    status) COMPREPLY=($(compgen -W '--only --versions' -- "$cur")) ;;
    deps) COMPREPLY=($(compgen -W 'install --only' -- "$cur")) ;;
  esac
}
complete -F _skytools skytools sky-tools

# Managed tools' scripts load on the first Tab for their command, so a new
# shell does not parse them all. Sourcing a script replaces this placeholder,
# and returning 124 makes bash retry the completion with the real one.
_skytools_complete_load() {
  local script=$HOME/.local/share/skytools/completions/bash/${1##*/}
  [ -r "$script" ] || return 1
  . "$script" >/dev/null 2>&1
  return 124
}
for _skytools_script in "$HOME/.local/share/skytools/completions/bash/"*; do
  [ -r "$_skytools_script" ] && complete -F _skytools_complete_load "${_skytools_script##*/}"
done
unset _skytools_script
