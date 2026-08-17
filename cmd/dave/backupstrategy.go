// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// backupStrategyNameEthSetHead is the only currently-supported
// --backup-strategy name: an Ethereum JSON-RPC endpoint that is rewound to
// the first block before the given timestamp via debug_setHead.
const backupStrategyNameEthSetHead = "ethsethead"

// backupStrategy is a single parsed --backup-strategy entry.
type backupStrategy struct {
	Name      string
	Endpoint  string
	Timestamp uint
}

// parseBackupStrategy parses a single "<name>:<endpoint>:<timestamp>"
// string. The endpoint may itself contain colons (e.g. a URL with a port),
// so the name is taken up to the first colon and the timestamp is taken
// after the last colon; everything in between is the endpoint.
func parseBackupStrategy(s string) (backupStrategy, error) {
	first := strings.Index(s, ":")
	last := strings.LastIndex(s, ":")
	if first == -1 || first == last {
		return backupStrategy{}, fmt.Errorf(
			"invalid backup strategy %q: expected format \"<name>:<endpoint>:<timestamp>\"", s)
	}

	name := s[:first]
	if name != backupStrategyNameEthSetHead {
		return backupStrategy{}, fmt.Errorf(
			"invalid backup strategy %q: name must be %q", s, backupStrategyNameEthSetHead)
	}

	endpoint := s[first+1 : last]
	if endpoint == "" {
		return backupStrategy{}, fmt.Errorf("invalid backup strategy %q: endpoint must not be empty", s)
	}

	tsStr := s[last+1:]
	ts, err := strconv.ParseUint(tsStr, 10, 0)
	if err != nil {
		return backupStrategy{}, fmt.Errorf("invalid backup strategy %q: invalid timestamp %q: %w", s, tsStr, err)
	}

	return backupStrategy{Name: name, Endpoint: endpoint, Timestamp: uint(ts)}, nil
}

// parseBackupStrategies parses each --backup-strategy flag value, returning
// the first error encountered.
func parseBackupStrategies(strs []string) ([]backupStrategy, error) {
	if len(strs) == 0 {
		return nil, nil
	}

	strategies := make([]backupStrategy, 0, len(strs))
	for _, s := range strs {
		bs, err := parseBackupStrategy(s)
		if err != nil {
			return nil, err
		}
		strategies = append(strategies, bs)
	}

	return strategies, nil
}
