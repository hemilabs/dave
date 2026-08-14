// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import "testing"

func TestParseBackupStrategyEntry(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		want    BackupStrategyEntry
		wantErr bool
	}{
		{
			name: "named seconds-ago",
			s:    "eth:12345",
			want: BackupStrategyEntry{Name: "eth", SecondsAgo: 12345},
		},
		{
			name: "zero seconds-ago",
			s:    "eth:0",
			want: BackupStrategyEntry{Name: "eth", SecondsAgo: 0},
		},
		{
			name:    "bare number without name",
			s:       "12345",
			wantErr: true,
		},
		{
			name:    "invalid seconds-ago",
			s:       "eth:notanumber",
			wantErr: true,
		},
		{
			name:    "negative seconds-ago",
			s:       "eth:-1",
			wantErr: true,
		},
		{
			name:    "empty name",
			s:       ":12345",
			wantErr: true,
		},
		{
			name:    "empty",
			s:       "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBackupStrategyEntry(tt.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseBackupStrategyEntry() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if got != tt.want {
				t.Errorf("ParseBackupStrategyEntry() got = %+v, want %+v", got, tt.want)
			}
		})
	}
}
