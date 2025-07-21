// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/hemilabs/dave/internal/dave"
)

const forgetHelp = `Removes snapshots according to a policy.

Usage:
  dave forget [flags] [snapshot ID...]

Flags:`

type forgetOptions struct {
	DryRun  bool
	Last    PolicyCount
	Hourly  PolicyCount
	Daily   PolicyCount
	Weekly  PolicyCount
	Monthly PolicyCount
	Yearly  PolicyCount
}

type forgetResult struct {
	Keep    []*dave.Snapshot  `json:"keep"`
	Remove  []*dave.Snapshot  `json:"remove"`
	Reasons []dave.KeepReason `json:"reasons"`
}

func runForget(ctx context.Context, args []string) (any, error) {
	var opts forgetOptions
	flag := newFlagSet("forget", forgetHelp)
	flag.BoolVarP(&opts.DryRun, "dry-run", "d", false, "Do not actually remove snapshots")
	flag.VarP(&opts.Last, "keep-last", "l", "keep the last n snapshots ('unlimited' to keep all)")
	flag.VarP(&opts.Hourly, "keep-hourly", "H", "keep the Last n hourly snapshots ('unlimited' to keep all)")
	flag.VarP(&opts.Daily, "keep-daily", "D", "keep the Last n daily snapshots ('unlimited' to keep all)")
	flag.VarP(&opts.Weekly, "keep-weekly", "w", "keep the Last weekly snapshots ('unlimited' to keep all)")
	flag.VarP(&opts.Monthly, "keep-monthly", "m", "keep the Last monthly snapshots ('unlimited' to keep all)")
	flag.VarP(&opts.Yearly, "keep-yearly", "y", "keep the Last yearly snapshots ('unlimited' to keep all)")

	if err := flagParse(flag, args); err != nil {
		return nil, err
	}
	args = flag.Args()

	repo, err := newRepo(ctx)
	if err != nil {
		return nil, err
	}
	d, err := dave.NewDave(repo, nil)
	if err != nil {
		return nil, err
	}

	// Retrieve snapshots.
	snapshots, err := d.SnapshotList(ctx)
	if err != nil {
		return nil, err
	}

	var removeIDs []string
	var result *forgetResult
	if len(args) > 0 {
		// If snapshot IDs are specified, ignore expire policy.
		removeIDs = append(removeIDs, args...)
	} else {
		policy := dave.ExpirePolicy{
			Last:    int(opts.Last),
			Hourly:  int(opts.Hourly),
			Daily:   int(opts.Daily),
			Weekly:  int(opts.Weekly),
			Monthly: int(opts.Monthly),
			Yearly:  int(opts.Yearly),
		}
		if policy.Empty() {
			return nil, errors.New("no expire policy specified")
		}

		keep, remove, reasons := dave.ApplyPolicy(snapshots, policy)
		printer.Pf("keep %d snapshots:\n", len(keep))
		printSnapshots(os.Stdout, keep, reasons, false, false)
		printer.Pf("\n")

		printer.Pf("remove %d snapshots:\n", len(remove))
		printSnapshots(os.Stdout, remove, nil, false, false)
		printer.Pf("\n")

		result = &forgetResult{
			Keep:    keep,
			Remove:  remove,
			Reasons: reasons,
		}
		for _, rm := range remove {
			removeIDs = append(removeIDs, rm.ID)
		}
	}

	// Check context before proceeding.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var mx sync.Mutex
	var failedIDs []string
	if len(removeIDs) > 0 {
		if opts.DryRun {
			printer.Pf("would remove %d snapshots: %v\n", len(removeIDs), removeIDs)
		} else {
			// Make Dave forget the snapshots.
			err = d.ParallelSnapshotRemove(ctx, removeIDs, func(id string, err error) error {
				if err != nil {
					printer.Ef("unable to remove snapshot %q: %v\n", id, err)
					mx.Lock()
					failedIDs = append(failedIDs, id)
					mx.Unlock()
					return nil
				}
				printer.Vf("removed snapshot %q\n", id)
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}

	if len(failedIDs) > 0 {
		return result, fmt.Errorf("failed to remove %d snapshots: %v", len(failedIDs), failedIDs)
	}
	return result, nil
}

type PolicyCount int

func (c *PolicyCount) String() string {
	if *c == -1 {
		return "unlimited"
	}
	return strconv.FormatInt(int64(*c), 10)
}

func (c *PolicyCount) Set(s string) error {
	switch s {
	case "unlimited":
		*c = -1
	default:
		val, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		if val < 0 {
			return errors.New("must be non-negative, use 'unlimited' to keep all")
		}
		*c = PolicyCount(val)
	}
	return nil
}

func (c *PolicyCount) Type() string {
	return "n"
}
