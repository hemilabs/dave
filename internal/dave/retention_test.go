// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"reflect"
	"testing"
	"time"
)

func TestApplyPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		policy     ExpirePolicy
		in         []*Snapshot
		wantKeep   []string // expected to be retained at end of test
		wantRemove []string // expected to NOT be retained at end of test
	}{
		{
			name:   "last 2",
			policy: ExpirePolicy{2, 0, 0, 0, 0, 0},
			in: []*Snapshot{
				testSnapshot(t, "1", "2025-04-12T11:00:00Z"), // +1 hour
				testSnapshot(t, "2", "2025-04-12T10:00:00Z"),
			},
			wantKeep: []string{"1", "2"},
		},
		{
			name:   "1 hourly",
			policy: ExpirePolicy{0, 1, 0, 0, 0, 0},
			in: []*Snapshot{
				testSnapshot(t, "1", "2025-04-12T10:30:00Z"), // +30 minutes
				testSnapshot(t, "2", "2025-04-12T10:00:00Z"),
			},
			wantKeep:   []string{"1"},
			wantRemove: []string{"2"},
		},
		{
			name:   "none",
			policy: ExpirePolicy{0, 0, 0, 0, 0, 0},
			in: []*Snapshot{
				testSnapshot(t, "1", "2025-04-12T11:00:00Z"), // +1 hour
				testSnapshot(t, "2", "2025-04-12T10:00:00Z"),
			},
			wantRemove: []string{"1", "2"},
		},
		{
			name:   "last 1 plus 2 hourly",
			policy: ExpirePolicy{1, 2, 0, 0, 0, 0},
			in: []*Snapshot{
				testSnapshot(t, "1", "2025-04-12T11:00:00Z"), // +1 hour
				testSnapshot(t, "2", "2025-04-12T10:30:00Z"), // +30 minutes
				testSnapshot(t, "3", "2025-04-12T10:00:00Z"),
			},
			wantKeep:   []string{"1", "2"},
			wantRemove: []string{"3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keep, rm, rs := ApplyPolicy(tt.in, tt.policy)

			keepIDs := transformSlice(keep, func(s *Snapshot) string { return s.ID })
			if !reflect.DeepEqual(keepIDs, tt.wantKeep) {
				t.Errorf("keep = %#v, want %#v (%#v)", keepIDs, tt.wantKeep, rs)
			}

			rmIDs := transformSlice(rm, func(s *Snapshot) string { return s.ID })
			if !reflect.DeepEqual(rmIDs, tt.wantRemove) {
				t.Errorf("rm = %#v, want %#v (%#v)", rmIDs, tt.wantRemove, rs)
			}
		})
	}
}

// transformSlice applies a transformation function to all elements in a slice.
func transformSlice[V, T any](in []V, fn func(v V) T) []T {
	if in == nil {
		return nil
	}
	o := make([]T, len(in))
	for i, v := range in {
		o[i] = fn(v)
	}
	return o
}

// testSnapshot creates a test snapshot with the given id and timestamp.
func testSnapshot(t *testing.T, id, ts string) *Snapshot {
	t.Helper()
	td, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	return &Snapshot{
		ID:   id,
		Time: td,
	}
}
