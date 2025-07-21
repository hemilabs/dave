// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"testing"
)

func TestCompressionTypeContentType(t *testing.T) {
	tests := []struct {
		name string
		ct   CompressionType
		want string
	}{
		// Make sure each CompressionType returns a Media Type registered with
		// IANA: https://www.iana.org/assignments/media-types/media-types.xhtml
		{
			name: "none",
			ct:   CompressionTypeNone,
			want: "",
		},
		{
			name: "gzip",
			ct:   CompressionTypeGzip,
			want: "application/gzip",
		},
		{
			name: "zstd",
			ct:   CompressionTypeZstd,
			want: "application/zstd",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ct.contentType(); got != tt.want {
				t.Errorf("contentType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCompressionType(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		want    CompressionType
		wantErr bool
	}{
		{
			name: "default",
			s:    "",
			want: DefaultCompressionType,
		},
		{
			name: "none",
			s:    "none",
			want: CompressionTypeNone,
		},
		{
			name: "gzip",
			s:    "gzip",
			want: CompressionTypeGzip,
		},
		{
			name: "zstd",
			s:    "zstd",
			want: CompressionTypeZstd,
		},
		{
			name:    "invalid",
			s:       "invalid",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCompressionType(tt.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCompressionType() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseCompressionType() got = %v, want %v", got, tt.want)
			}
		})
	}
}
