// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/hemilabs/dave/internal/dave"
)

const daveHelp = `Dave is a backup program.

Usage:
  dave [command]

Commands:
  backup     Creates a new backup of directories
  forget     Removes snapshots from the repository
  ls         Lists all snapshots

Global Flags:`

type globalOptions struct {
	Help      bool
	JSON      bool
	Repo      string
	Verbosity int
	Quiet     bool
}

var (
	printer = newPrinter(os.Stderr, 1)

	gflag = pflag.NewFlagSet("dave", pflag.ContinueOnError)
	gopts globalOptions
)

func run() error {
	gflag.BoolVarP(&gopts.Help, "help", "h", false, "shows help")
	gflag.BoolVar(&gopts.JSON, "json", false, "output JSON")
	gflag.BoolVarP(&gopts.Quiet, "quiet", "q", false, "suppress output")
	gflag.StringVarP(&gopts.Repo, "repo", "r", "", "repository to store backup")
	gflag.CountVarP(&gopts.Verbosity, "verbose", "v", "verbosity level (can specify multiple times or -v=n)")
	gflag.Usage = flagUsage(nil, daveHelp)

	if len(os.Args) < 2 {
		// Sub-command was not specified.
		gflag.Usage()
		return nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var (
		val any
		err error
	)
	subCmd, args := parseSubcommand(os.Args[1:])
	switch subCmd {
	case "backup":
		val, err = runBackup(ctx, args)
	case "forget":
		val, err = runForget(ctx, args)
	case "ls", "list":
		val, err = runList(ctx, args)
	default:
		gflag.Usage()
		return fmt.Errorf("unknown command %q", gflag.Arg(0))
	}

	// Use context error if err is nil.
	if err == nil || errors.Is(err, pflag.ErrHelp) {
		err = ctx.Err()
	}

	if err == nil && val != nil && gopts.JSON {
		b, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			return err
		}
		// Specifically write to stdout, so it can be piped to a file.
		_, _ = fmt.Fprintf(os.Stdout, "%s", string(b))
		return nil
	}
	return err
}

func newRepo(ctx context.Context) (dave.Repository, error) {
	if gopts.Repo == "" {
		return nil, errors.New("please specify repository (-r or --repo)")
	}
	printer.VVf("using repository: %q\n", gopts.Repo)
	return dave.NewRepository(ctx, gopts.Repo)
}

// parseSubcommand finds the first non-flag argument and returns the remaining args.
func parseSubcommand(args []string) (string, []string) {
	var skipNext bool
	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}

		switch {
		case strings.HasPrefix(arg, "--") && !strings.Contains(arg, "="):
			// --flag arg
			skipNext = true
		case strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") && len(arg) == 2:
			// -f arg
			skipNext = true
		case arg != "" && !strings.HasPrefix(arg, "-"):
			return arg, append(args[:i], args[i+1:]...)
		}
	}

	return "", args
}

// newFlagSet creates a new flag set with a usage function and the global flagset.
func newFlagSet(name, help string) *pflag.FlagSet {
	f := pflag.NewFlagSet(name, pflag.ContinueOnError)
	f.Usage = flagUsage(f, help)
	f.AddFlagSet(gflag)
	return f
}

// flagUsage is a pflag.Usage function.
func flagUsage(flag *pflag.FlagSet, help string) func() {
	return func() {
		_, _ = fmt.Fprintf(os.Stderr, "%s", help)
		fmt.Println()
		if flag != nil {
			flag.PrintDefaults()
			_, _ = fmt.Fprintln(os.Stderr, "\nGlobal Flags:")
		}
		gflag.PrintDefaults()
	}
}

// flagParse parses the flags from the given arguments and handles global flags.
func flagParse(flag *pflag.FlagSet, args []string) error {
	if err := flag.Parse(args); err != nil {
		flag.Usage()
		return err
	}

	// Handle global flags here.
	if gopts.Help {
		flag.Usage()
		return pflag.ErrHelp
	}

	if gopts.Quiet && gopts.Verbosity > 0 {
		return errors.New("--quiet and --verbosity cannot be used together")
	}
	printer.SetVerbosity(gopts.Verbosity)
	printer.SetQuiet(gopts.Quiet)

	return nil
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
