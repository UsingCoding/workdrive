package shellintegration

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed wrapper.sh
var wrapper string

// Init emits opt-in initialization only; it never edits shell startup files.
// Zsh completion is registered only when compdef is already available, so put
// initialization after compinit. Reinitialization retains shell-local history.
func Init(shell string) (string, error) {
	var header, setup, restore, completion string
	switch shell {
	case "bash":
		header = `# workdrive Bash integration; source in ~/.bashrc.
# History belongs to this shell, never to its children.
if [[ ${_workdrive_previous_workspace+x} != x ]]; then
    _workdrive_previous_workspace=''
fi
builtin export -n _workdrive_previous_workspace
`
		setup = `    local _wd_saved_int
    _wd_saved_int=$(builtin trap -p INT)
    # Only the shell's own trusted trap declaration is evaluated, never handoff data.
    builtin trap 'if [[ -n ${_wd_tmp-} ]]; then command rm -rf -- "$_wd_tmp" || :; fi; if [[ -n $_wd_saved_int ]]; then builtin eval -- "$_wd_saved_int"; else builtin trap - INT; fi; return 130' INT`
		restore = `    if [[ -n $_wd_saved_int ]]; then
        builtin eval -- "$_wd_saved_int"
    else
        builtin trap - INT
    fi`
		completion = `# Cobra owns parsing and completion registration. Evaluate only its static
# initialization code; Bash 3.2 cannot reliably source process substitutions.
# Runtime navigation records are never evaluated or sourced.
builtin eval -- "$(command workdrive completion bash)"
`
	case "zsh":
		header = `# workdrive Zsh integration; source in ~/.zshrc, after compinit.
# Retain the value on reinitialization and explicitly remove its export attribute.
typeset -g +x _workdrive_previous_workspace
`
		setup = `    emulate -L zsh
    setopt localtraps
    builtin trap 'if [[ -n ${_wd_tmp-} ]]; then command rm -rf -- "$_wd_tmp" || :; fi; return 130' INT`
		restore = ""
		completion = `# Do not initialize or change the user's completion system implicitly.
if (( $+functions[compdef] )); then
    builtin source <(command workdrive completion zsh)
fi
`
	default:
		return "", fmt.Errorf("unsupported shell %q: supported shells are bash and zsh", shell)
	}
	return header + strings.NewReplacer("# @SETUP@", setup, "# @RESTORE@", restore).Replace(wrapper) + completion, nil
}
