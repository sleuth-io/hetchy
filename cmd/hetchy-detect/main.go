// hetchy-detect scans a repository and emits the "hints" payload that
// the bootstrap LLM prompt will be seeded with. It is intentionally
// pure-Go, deterministic, and side-effect-free — its job is to find
// what's there, not to interpret it.
//
// Usage:
//
//	hetchy-detect [path]   # path defaults to "."
//
// Output is JSON on stdout. Run with --pretty to indent.
//
// This is a thin wrapper around internal/bootstrap.Detect so the same
// detection logic that runs inside the bootstrap loop can also be run
// from the shell against any repo for debugging the prompt context.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hetchyhq/hetchy/internal/bootstrap"
)

func main() {
	pretty := flag.Bool("pretty", false, "indent JSON output")
	prompt := flag.Bool("prompt", false, "emit the rendered bootstrap prompt instead of JSON hints")
	ownerRepo := flag.String("repo", "", "owner/repo identifier for the prompt (defaults to repo's go module or directory name)")
	flag.Parse()

	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		fail("resolve path: %v", err)
	}

	hints, err := bootstrap.Detect(abs)
	if err != nil {
		fail("detect: %v", err)
	}

	if *prompt {
		identity := *ownerRepo
		if identity == "" {
			identity = inferIdentity(hints, abs)
		}
		fmt.Print(bootstrap.BuildPrompt(hints, bootstrap.PromptArgs{
			OwnerRepo: identity,
		}))
		return
	}

	enc := json.NewEncoder(os.Stdout)
	if *pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(hints); err != nil {
		fail("encode: %v", err)
	}
}

func inferIdentity(hints *bootstrap.Hints, root string) string {
	if hints != nil && hints.GoMod != nil && hints.GoMod.Module != "" {
		return hints.GoMod.Module
	}
	return filepath.Base(root)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "hetchy-detect: "+format+"\n", args...)
	os.Exit(1)
}
