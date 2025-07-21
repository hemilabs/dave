// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/hemilabs/dave/internal/dave"
)

const listHelp = `Lists snapshots stored in a repository.

Usage:
  dave ls [flags]

Flags:`

func runList(ctx context.Context, args []string) (any, error) {
	flag := newFlagSet("list", listHelp)
	if err := flagParse(flag, args); err != nil {
		return nil, err
	}
	args = flag.Args()

	if len(args) != 0 {
		flag.Usage()
		return nil, errors.New("too many arguments")
	}

	repo, err := newRepo(ctx)
	if err != nil {
		return nil, err
	}
	d, err := dave.NewDave(repo, nil)
	if err != nil {
		return nil, err
	}

	// Retrieve snapshots from repository.
	printer.VVf("retrieving snapshots\n")
	snapshots, err := d.SnapshotList(ctx)
	if err != nil {
		return nil, err
	}

	if !gopts.JSON {
		printSnapshots(os.Stdout, snapshots, nil, true, true)
	}
	return snapshots, nil
}

func printSnapshots(w io.Writer, snapshots []*dave.Snapshot, reasons []dave.KeepReason, withSize, withFooter bool) {
	keepReasons := make(map[string]dave.KeepReason, len(reasons))
	if len(reasons) > 0 {
		for _, reason := range reasons {
			keepReasons[reason.Snapshot.ID] = reason
		}
	}

	// Sort oldest to newest
	slices.SortStableFunc(snapshots, func(a, b *dave.Snapshot) int {
		return a.Time.Compare(b.Time)
	})

	t := newTable()
	t.AddColumn("Snapshot ID", "{{ .ID }}")
	t.AddColumn("Time", "{{ .Time }}")
	t.AddColumn("Archives", `{{ join .Archives "\n" }}`)
	if reasons != nil {
		t.AddColumn("Keep Reason", `{{ join .KeepReasons "\n" }}`)
	}

	type snapshot struct {
		ID          string
		Time        string
		Archives    []string
		KeepReasons []string
	}
	for _, snap := range snapshots {
		row := snapshot{
			ID:          snap.ID,
			Time:        snap.Time.Format(time.DateTime),
			Archives:    make([]string, len(snap.Archives)),
			KeepReasons: keepReasons[snap.ID].Reasons,
		}
		for i, archive := range snap.Archives {
			if withSize {
				row.Archives[i] = fmt.Sprintf("%s (%s)", archive.Name,
					humanize.IBytes(archive.Size))
				continue
			}
			row.Archives[i] = archive.Name
		}
		t.AddRow(row)
	}

	if withFooter {
		t.AddFooter(fmt.Sprintf("%d snapshots", len(snapshots)))
	}

	if err := t.Render(w); err != nil {
		panic(err)
	}
}
