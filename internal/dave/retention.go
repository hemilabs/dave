// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"slices"
	"time"
)

// ExpirePolicy defines a policy used to manage retention of snapshots.
type ExpirePolicy struct {
	Last    int // keep last n snapshots
	Hourly  int // keep last n hourly snapshots
	Daily   int // keep last n daily snapshots
	Weekly  int // keep last n weekly snapshots
	Monthly int // keep last n monthly snapshots
	Yearly  int // keep last n yearly snapshots
}

// Empty returns whether the policy is empty, meaning all snapshots would be
// removed if used.
func (p ExpirePolicy) Empty() bool {
	return p == ExpirePolicy{}
}

// KeepReason contains the reason(s) why a snapshot is being kept.
type KeepReason struct {
	Snapshot *Snapshot `json:"snapshot"`
	Reasons  []string  `json:"reasons"`
}

type expiryBucket struct {
	reason   string
	count    int
	bucketer func(t time.Time, i int) int
	last     int
}

// ApplyPolicy applies an expiry policy to a list of snapshots, and returns the
// snapshots to keep and snapshots to remove.
func ApplyPolicy(snapshots []*Snapshot, p ExpirePolicy) (keep, remove []*Snapshot, reasons []KeepReason) {
	// Sort snapshots newest to oldest.
	slices.SortStableFunc(snapshots, func(a, b *Snapshot) int {
		return b.Time.Compare(a.Time)
	})

	buckets := []*expiryBucket{
		{"last snapshot", p.Last, index, -1},
		{"hourly snapshot", p.Hourly, hourly, -1},
		{"daily snapshot", p.Daily, daily, -1},
		{"weekly snapshot", p.Weekly, weekly, -1},
		{"monthly snapshot", p.Monthly, monthly, -1},
		{"yearly snapshot", p.Yearly, yearly, -1},
	}

	for i, s := range snapshots {
		var (
			keepSnapshot bool
			keepReasons  []string
		)

		for _, b := range buckets {
			if b.count > 0 || b.count == -1 {
				val := b.bucketer(s.Time, i)
				if val != b.last || i == len(snapshots)-1 {
					keepSnapshot = true
					b.last = val
					if b.count > 0 {
						b.count--
					}
					keepReasons = append(keepReasons, b.reason)
				}
			}
		}

		if keepSnapshot {
			keep = append(keep, s)
			reasons = append(reasons, KeepReason{
				Snapshot: s,
				Reasons:  keepReasons,
			})
			continue
		}
		remove = append(remove, s)
	}

	return keep, remove, reasons
}

// index returns the index.
func index(_ time.Time, i int) int {
	return i
}

// hourly returns an integer containing YYYYMMDDHH.
func hourly(t time.Time, _ int) int {
	return t.Year()*1e6 + int(t.Month())*1e4 + t.Day()*1e2 + t.Hour()
}

// daily returns an integer containing YYYYMMDD.
func daily(t time.Time, _ int) int {
	return t.Year()*1e4 + int(t.Month())*1e2 + t.Day()
}

// weekly returns an integer containing YYYYWW.
func weekly(t time.Time, _ int) int {
	year, week := t.ISOWeek()
	return year*1e2 + week
}

// monthly returns an integer containing YYYYMM.
func monthly(t time.Time, _ int) int {
	return t.Year()*1e2 + int(t.Month())
}

// yearly returns the year of the given time.
func yearly(t time.Time, _ int) int {
	return t.Year()
}
