// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"bytes"
	"encoding/json"
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

func TestCompressionTypeJSONMarshal(t *testing.T) {
	tests := []struct {
		name string
		ct   CompressionType
		want []byte
	}{
		{
			name: "default",
			ct:   DefaultCompressionType,
			want: []byte("\"gzip\""),
		},
		{
			name: "none",
			ct:   CompressionTypeNone,
			want: []byte("\"none\""),
		},
		{
			name: "gzip",
			ct:   CompressionTypeGzip,
			want: []byte("\"gzip\""),
		},
		{
			name: "zstd",
			ct:   CompressionTypeZstd,
			want: []byte("\"zstd\""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.ct)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(b, tt.want) {
				t.Fatalf("expected bytes %s, got %s", tt.want, b)
			}

			r := bytes.NewReader(b)
			var newCt CompressionType
			if err := json.NewDecoder(r).Decode(&newCt); err != nil {
				t.Fatal(err)
			}

			if newCt != tt.ct {
				t.Fatalf("expected new type %s, got %s", tt.ct, newCt)
			}
		})
	}
}
