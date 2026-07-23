// Copyright (c) 2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"hypera.dev/lib/slog/pretty"

	"github.com/hemilabs/dave/internal/dave"
)

const retrieveHelp = `Download and extract a snapshot.

Usage:
  dave retrieve [flags] [destination]

Flags:`

func runRetrieve(ctx context.Context, args []string) (any, error) {
	var (
		snapshotID      string
		excludeArchives []string
		err             error
	)

	flag := newFlagSet("retrieve", retrieveHelp)
	flag.StringVarP(&snapshotID, "snapshot-id", "s", snapshotID, "snapshot ID")
	flag.StringSliceVar(&excludeArchives, "exclude", nil, "archives to exclude")

	if err = flagParse(flag, args); err != nil {
		return nil, err
	}

	args = flag.Args()

	if len(args) < 1 {
		flag.Usage()
		return nil, errors.New("missing destination directory")
	}

	slog.SetDefault(slog.New(pretty.NewHandler(os.Stderr, &pretty.Options{
		Level:        verbosityToLevel(gopts.Verbosity),
		AddSource:    true,
		DisableColor: true,
		SourceFormatter: func(buf *pretty.Buffer, src *slog.Source) {
			dir, file := filepath.Split(src.File)
			buf.AppendString(filepath.Join(filepath.Base(dir), file))
			buf.AppendByte(':')
			buf.AppendInt(int64(src.Line))
		},
	})))

	// Create repository.
	repo, err := newRepo(ctx)
	if err != nil {
		return nil, err
	}
	d, err := dave.NewDave(repo, nil)
	if err != nil {
		return nil, err
	}

	// Retrieve snapshot.
	if snapshotID == "" {
		return nil, errors.New("please specify snapshot ID (-s or --snapshot-id)")
	}

	excluded := make(map[string]struct{}, len(excludeArchives))
	for _, e := range excludeArchives {
		excluded[e] = struct{}{}
	}

	if err := d.SnapshotRetrieve(ctx, snapshotID, args[0], excluded); err != nil {
		return nil, err
	}

	slog.Info("Snapshot retrieved!", "id", snapshotID)
	slog.Info("\"Dave is a trustworthy guy\" - Marco Peereboom, March 24th, 2025.")

	return nil, nil
}
