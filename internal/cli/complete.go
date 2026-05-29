package cli

import (
	"context"
	"fmt"
	"os"
)

// completionSubcommands is the static list offered as the first argument,
// alongside the dynamic node names (a bare `macker <node>` attaches).
var completionSubcommands = []string{
	"ls", "exec", "grid", "attach", "kill", "agent",
	"collector", "context", "version", "completion", "help",
}

// cmdComplete is a hidden, machine-readable helper the shell completion script
// calls to get dynamic candidates. It prints one candidate per line and never
// errors loudly (completion must stay quiet).
//
//	macker __complete subcommands
//	macker __complete nodes
//	macker __complete sessions <node>
func cmdComplete(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "subcommands":
		for _, s := range completionSubcommands {
			fmt.Println(s)
		}
	case "nodes":
		r, err := newResolver(ctx)
		if err != nil {
			return nil
		}
		for _, n := range r.nodes {
			fmt.Println(n.Name)
		}
	case "sessions":
		if len(args) < 2 {
			return nil
		}
		r, err := newResolver(ctx)
		if err != nil {
			return nil
		}
		res, err := r.resolve(parseTarget(args[1]))
		if err != nil {
			return nil
		}
		names, err := listSessionNames(ctx, r, res)
		if err != nil {
			return nil
		}
		for _, n := range names {
			fmt.Println(n)
		}
	}
	return nil
}

// cmdCompletion prints a shell completion script. Currently zsh only.
//
//	eval "$(macker completion zsh)"   # add to ~/.zshrc after compinit
func cmdCompletion(args []string) error {
	shell := "zsh"
	if len(args) > 0 {
		shell = args[0]
	}
	switch shell {
	case "zsh":
		fmt.Print(zshCompletionScript)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "macker completion: unsupported shell %q (try: zsh)\n", shell)
		return fmt.Errorf("unsupported shell %q", shell)
	}
}

// zshCompletionScript defines and registers a `_macker` completion. It is
// eval-friendly: `eval "$(macker completion zsh)"` in ~/.zshrc (after compinit)
// works, as does saving it to a file named _macker on $fpath.
const zshCompletionScript = `#compdef macker
# macker zsh completion. Install with:
#   eval "$(macker completion zsh)"   # in ~/.zshrc, after 'autoload -Uz compinit && compinit'
_macker() {
  local -a subcmds nodes
  subcmds=(${(f)"$(macker __complete subcommands 2>/dev/null)"})

  if (( CURRENT == 2 )); then
    local cur=${words[CURRENT]}
    if [[ $cur == *:* ]]; then
      # node:session — complete sessions on that node
      local node=${cur%%:*}
      local -a sess
      sess=(${(f)"$(macker __complete sessions $node 2>/dev/null)"})
      compadd -P "${node}:" -- $sess
      return
    fi
    nodes=(${(f)"$(macker __complete nodes 2>/dev/null)"})
    _describe -t subcommands 'macker subcommand' subcmds
    _describe -t nodes 'node' nodes
    return
  fi

  case ${words[2]} in
    exec|kill|attach|grid)
      nodes=(${(f)"$(macker __complete nodes 2>/dev/null)"})
      _describe -t nodes 'node' nodes
      ;;
    context)
      (( CURRENT == 3 )) && compadd -- ls use
      ;;
    completion)
      (( CURRENT == 3 )) && compadd -- zsh
      ;;
    *)
      # bare node form: macker <node> <clear|ls|session|index>
      if (( CURRENT == 3 )); then
        local node=${words[2]%%:*}
        local -a sess
        sess=(${(f)"$(macker __complete sessions $node 2>/dev/null)"})
        compadd -- clear ls
        _describe -t sessions 'session' sess
      fi
      ;;
  esac
}
compdef _macker macker
`
