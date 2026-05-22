package cli

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/masakasuno1/macker/internal/config"
)

// cmdContext shows or switches the active config context.
//
//	macker context            show contexts and the current one
//	macker context ls         list contexts
//	macker context use <name> set the current context
func cmdContext(_ context.Context, args []string) error {
	sub := "ls"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "ls", "":
		names, current, err := config.Contexts()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Printf("no contexts configured; using the implicit %q context\n", config.DefaultContextName)
			return nil
		}
		sort.Strings(names)
		for _, n := range names {
			marker := "  "
			if n == current {
				marker = "* "
			}
			fmt.Printf("%s%s\n", marker, n)
		}
		return nil

	case "use":
		if len(args) != 2 {
			return fmt.Errorf("usage: macker context use <name>")
		}
		if err := config.SetCurrentContext(args[1]); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "macker: switched to context %q\n", args[1])
		return nil

	default:
		return fmt.Errorf("unknown context subcommand %q (want: ls, use)", sub)
	}
}
