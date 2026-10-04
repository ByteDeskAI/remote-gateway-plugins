// Command release enforces the plugin contract and versions, changelogs and
// tags the plugins under plugins/. See docs/CONTRACT.md.
//
//	release check [--commits BASE..HEAD]
//	release plan --mode dev|release [--build N]
//	release apply --id ID --version X.Y.Z
//	release stamp --id ID --version V --dir STAGE
//	release finalize --ids a,b
//
// Every subcommand takes --repo (default: the Git checkout holding the
// current directory).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const usage = `usage: release <command> [flags]

  check [--commits BASE..HEAD]          check every plugin against docs/CONTRACT.md,
                                        and optionally the scopes of a commit range
  plan --mode dev|release [--build N]   print the plugins to release, as JSON
  apply --id ID --version X.Y.Z         write VERSION, plugin.json and CHANGELOG.md
  stamp --id ID --version V --dir DIR   set the version in DIR/plugin.json only
  finalize --ids a,b                    commit the applied files once and tag each plugin

Every command takes --repo DIR (default: the Git checkout of the current directory).
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("missing command")
	}
	fs := flag.NewFlagSet("release "+args[0], flag.ContinueOnError)
	repo := fs.String("repo", "", "repository root")
	commits := fs.String("commits", "", "check: commit range BASE..HEAD")
	mode := fs.String("mode", "", "plan: dev or release")
	build := fs.Int("build", -1, "plan: build number for dev versions")
	id := fs.String("id", "", "plugin id")
	ver := fs.String("version", "", "version to write")
	dir := fs.String("dir", "", "stamp: staging directory")
	ids := fs.String("ids", "", "finalize: comma-separated plugin ids")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	root := *repo
	if root == "" && args[0] != "stamp" {
		out, err := git(".", "rev-parse", "--show-toplevel")
		if err != nil {
			return err
		}
		root = strings.TrimSpace(out)
	}

	switch args[0] {
	case "check":
		pluginIDsFound, err := pluginIDs(root)
		if err != nil {
			return err
		}
		var problems []string
		for _, p := range pluginIDsFound {
			problems = append(problems, checkPlugin(root, p)...)
		}
		if *commits != "" {
			more, err := checkCommits(root, *commits)
			if err != nil {
				return err
			}
			problems = append(problems, more...)
		}
		if len(problems) > 0 {
			for _, p := range problems {
				fmt.Println("  -", p)
			}
			return fmt.Errorf("%d problem(s) found", len(problems))
		}
		fmt.Printf("ok: %d plugin(s) meet the contract\n", len(pluginIDsFound))
		return nil

	case "plan":
		if *mode != "dev" && *mode != "release" {
			return errors.New("--mode must be dev or release")
		}
		if *mode == "dev" && *build < 0 {
			return errors.New("--build N is required in dev mode")
		}
		plans, err := planAll(root, *mode, *build)
		if err != nil {
			return err
		}
		out, _ := json.MarshalIndent(plans, "", "  ")
		fmt.Println(string(out))
		return nil

	case "apply":
		if err := requireID(*id); err != nil {
			return err
		}
		return apply(root, *id, *ver, today())

	case "stamp":
		if err := requireID(*id); err != nil {
			return err
		}
		if !semverPrePattern.MatchString(*ver) {
			return fmt.Errorf("--version must be SemVer, got %q", *ver)
		}
		if *dir == "" {
			return errors.New("--dir is required")
		}
		return writeManifestVersion(filepath.Join(*dir, "plugin.json"), *id, *ver)

	case "finalize":
		var list []string
		for _, s := range strings.Split(*ids, ",") {
			if s = strings.TrimSpace(s); s != "" {
				if err := requireID(s); err != nil {
					return err
				}
				list = append(list, s)
			}
		}
		if len(list) == 0 {
			return errors.New("--ids is required")
		}
		return finalize(root, list)
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", args[0])
}

func requireID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("--id must be a lowercase kebab-case plugin id, got %q", id)
	}
	return nil
}
