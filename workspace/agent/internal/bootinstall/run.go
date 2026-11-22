package bootinstall

import (
	"context"
	"fmt"
	"os"
)

// Run is `workspace-agent boot-install <step> [--repin] [--present] [cli...]`: one step of the
// entrypoint's lean boot-install. Steps: rtk, agy, cursor, agy-version (prints the installed agy's version), npm (the four npm CLIs), npm-recover [--discard] (settle a killed self-update's backups; --discard also drops an accepted update's), npm-verify (the repair
// pass; --present limits it to CLIs with a ~/.local copy). Exit 1 is a failed install, 3 a skip
// for lack of space (the entrypoint then holds back its other downloads), 2 a usage error.
func Run(args []string) {
	repin, present, discard := false, false, false
	var names []string
	for _, a := range args {
		switch a {
		case "--repin":
			repin = true
		case "--present":
			present = true
		case "--discard":
			discard = true
		default:
			names = append(names, a)
		}
	}
	usage := func() {
		fmt.Fprintln(os.Stderr, "usage: workspace-agent boot-install rtk|agy|cursor|npm [--repin] | agy-version | self-update | npm-recover [--discard] | npm-verify [--present] [claude|opencode|codex|copilot...]")
		os.Exit(2)
	}
	if len(names) == 0 {
		usage()
	}
	step, rest := names[0], names[1:]
	if (step == "agy-version" || step == "self-update" || step == "npm-recover") && (len(rest) > 0 || repin || present) {
		usage()
	}
	if discard && step != "npm-recover" {
		usage()
	}
	if (step != "npm-verify" && step != "agy-version" && len(rest) > 0) || (present && step != "npm-verify") || (repin && step == "npm-verify") {
		usage()
	}
	if present && len(rest) > 0 {
		usage()
	}
	if do, keep := npmRecoverPlan(step, present, discard); do {
		DefaultEnv(false).npmRecoverAside(keep)
	}
	switch step {
	case "rtk":
		os.Exit(RTK(context.Background(), DefaultEnv(repin)))
	case agyTool:
		os.Exit(AGY(context.Background(), DefaultEnv(repin)))
	case cursorStatusName:
		os.Exit(Cursor(context.Background(), DefaultEnv(repin)))
	case "self-update":
		os.Exit(SelfUpdate(context.Background(), DefaultEnv(false)))
	case "agy-version":
		fmt.Println(DefaultEnv(false).AgyEffectiveVersion())
	case "npm":
		os.Exit(NPM(DefaultEnv(repin)))
	case "npm-recover":
		// Recovery already ran above; the entrypoint calls this before its baked/OFF branch can
		// uninstall the npm CLIs.
	case "npm-verify":
		e := DefaultEnv(false)
		if present {
			os.Exit(e.NPMVerifyPresentStep())
		}
		for _, c := range rest {
			if npmPkg(c) == "" {
				fmt.Fprintf(os.Stderr, "boot-install: %q is not an npm CLI\n", c)
				os.Exit(2)
			}
		}
		if len(rest) == 0 {
			usage()
		}
		os.Exit(e.NPMVerify(rest, false))
	default:
		fmt.Fprintf(os.Stderr, "boot-install: unknown step %q (have: rtk, agy, cursor, agy-version, self-update, npm, npm-recover, npm-verify)\n", step)
		os.Exit(2)
	}
}

// npmRecoverPlan says whether a step settles a killed self-update's backups first, and whether an
// accepted update's backups stay for the every-start verify pass. Only the opt-in OFF start
// (npm-recover --discard) drops them unverified: its copies are about to be uninstalled, so there
// is nothing left to verify them against.
func npmRecoverPlan(step string, present, discard bool) (do, keepAccepted bool) {
	switch step {
	case "npm", "npm-verify", "self-update", "npm-recover":
	default:
		return false, false
	}
	if step == "npm-verify" && present {
		return false, false // the pass that settles them itself
	}
	return true, !(step == "npm-recover" && discard)
}
