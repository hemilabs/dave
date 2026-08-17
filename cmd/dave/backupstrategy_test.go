// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"reflect"
	"testing"
)

func TestParseBackupStrategies(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []backupStrategy
		wantErr bool
	}{
		{
			name:  "empty",
			input: nil,
			want:  nil,
		},
		{
			name:  "one",
			input: []string{"ethsethead:http://localhost:8545:1700000000"},
			want: []backupStrategy{
				{Name: "ethsethead", Endpoint: "http://localhost:8545", Timestamp: 1700000000},
			},
		},
		{
			name: "many",
			input: []string{
				"ethsethead:http://localhost:8545:1700000000",
				"ethsethead:https://host-b.example.com:8545:1700100000",
				"ethsethead:some-opaque-endpoint:1700200000",
			},
			want: []backupStrategy{
				{Name: "ethsethead", Endpoint: "http://localhost:8545", Timestamp: 1700000000},
				{Name: "ethsethead", Endpoint: "https://host-b.example.com:8545", Timestamp: 1700100000},
				{Name: "ethsethead", Endpoint: "some-opaque-endpoint", Timestamp: 1700200000},
			},
		},
		{
			name:    "invalid - no colons",
			input:   []string{"justastring"},
			wantErr: true,
		},
		{
			name:    "invalid - only one colon",
			input:   []string{"ethsethead:endpoint"},
			wantErr: true,
		},
		{
			name:    "invalid - empty name",
			input:   []string{":endpoint:123"},
			wantErr: true,
		},
		{
			name:    "invalid - wrong name",
			input:   []string{"nightly:endpoint:123"},
			wantErr: true,
		},
		{
			name:    "invalid - empty endpoint",
			input:   []string{"ethsethead::123"},
			wantErr: true,
		},
		{
			name:    "invalid - non-numeric timestamp",
			input:   []string{"ethsethead:endpoint:notanumber"},
			wantErr: true,
		},
		{
			name:    "invalid - negative timestamp",
			input:   []string{"ethsethead:endpoint:-1"},
			wantErr: true,
		},
		{
			name: "invalid - one bad entry among good ones",
			input: []string{
				"ethsethead:http://localhost:8545:1700000000",
				"broken",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBackupStrategies(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseBackupStrategies(%v) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBackupStrategies(%v) unexpected error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseBackupStrategies(%v) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

// TestParseBackupStrategyNameRestriction ensures a --backup-strategy entry's
// name can ONLY be "ethsethead" — the sole currently-supported strategy.
func TestParseBackupStrategyNameRestriction(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "exact match ethsethead", input: "ethsethead:http://localhost:8545:1700000000"},
		{name: "wrong name eth", input: "eth:http://localhost:8545:1700000000", wantErr: true},
		{name: "wrong case EthSetHead", input: "EthSetHead:http://localhost:8545:1700000000", wantErr: true},
		{name: "wrong case ETHSETHEAD", input: "ETHSETHEAD:http://localhost:8545:1700000000", wantErr: true},
		{name: "arbitrary name", input: "nightly:http://localhost:8545:1700000000", wantErr: true},
		{name: "empty name", input: ":http://localhost:8545:1700000000", wantErr: true},
		{name: "name with trailing space", input: "ethsethead :http://localhost:8545:1700000000", wantErr: true},
		{name: "name as substring", input: "ethsetheadx:http://localhost:8545:1700000000", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bs, err := parseBackupStrategy(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseBackupStrategy(%q) = %+v, want error", tc.input, bs)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBackupStrategy(%q) unexpected error: %v", tc.input, err)
			}
			if bs.Name != backupStrategyNameEthSetHead {
				t.Fatalf("parseBackupStrategy(%q).Name = %q, want %q", tc.input, bs.Name, backupStrategyNameEthSetHead)
			}
		})
	}
}
