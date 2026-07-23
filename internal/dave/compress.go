// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type CompressionType int

const (
	// CompressionTypeNone disables compression. Uncompressed tar archives will
	// be created.
	CompressionTypeNone CompressionType = iota

	// CompressionTypeGzip uses Gzip compression for archives (tar.gz).
	CompressionTypeGzip

	// CompressionTypeZstd uses Zstandard compression for archives (tar.zst).
	// Zstandard is a fast compression algorithm, providing high compression
	// ratios. It is significantly faster than gzip, but less supported.
	CompressionTypeZstd
)

// DefaultCompressionType is the default compression type.
const DefaultCompressionType = CompressionTypeGzip

// ParseCompressionType parses a CompressionType from its string representation.
func ParseCompressionType(s string) (CompressionType, error) {
	switch strings.ToLower(s) {
	case "":
		return DefaultCompressionType, nil
	case "none":
		return CompressionTypeNone, nil
	case "gzip":
		return CompressionTypeGzip, nil
	case "zstd":
		return CompressionTypeZstd, nil
	default:
		return CompressionTypeNone, fmt.Errorf("unknown compression type: %q", s)
	}
}

// String returns a string representation of the compression type.
func (ct CompressionType) String() string {
	switch ct {
	case CompressionTypeNone:
		return "none"
	case CompressionTypeGzip:
		return "gzip"
	case CompressionTypeZstd:
		return "zstd"
	default:
		return ""
	}
}

func (ct CompressionType) MarshalJSON() ([]byte, error) {
	s := ct.String()
	if s == "" {
		return nil, fmt.Errorf("unknown compression type: %d", ct)
	}
	return json.Marshal(s)
}

func (ct *CompressionType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseCompressionType(s)
	if err != nil {
		return err
	}
	*ct = parsed
	return nil
}

// contentType returns the [IANA-registered Media Type] for the compression
// output data.
//
// [IANA-registered Media Type]: https://www.iana.org/assignments/media-types/media-types.xhtml
func (ct CompressionType) contentType() string {
	switch ct {
	case CompressionTypeGzip:
		return "application/gzip"
	case CompressionTypeZstd:
		return "application/zstd"
	default:
		return ""
	}
}

// fileExtension returns the file extension used for the compression type.
func (ct CompressionType) fileExtension() string {
	switch ct {
	case CompressionTypeGzip:
		return ".gz"
	case CompressionTypeZstd:
		return ".zst"
	default:
		return ""
	}
}

// newCompressionEncoder creates a new encoder for the given compression type.
func newCompressionEncoder(ct CompressionType, dst io.Writer) (io.WriteCloser, error) {
	switch ct {
	case CompressionTypeNone:
		return nopWriteCloser{dst}, nil
	case CompressionTypeGzip:
		return gzip.NewWriter(dst), nil
	case CompressionTypeZstd:
		return zstd.NewWriter(dst)
	default:
		return nil, errors.New("unknown compression type")
	}
}

// newCompressionDecoder creates a new decoder for the given compression type.
func newCompressionDecoder(ct CompressionType, src io.Reader) (io.ReadCloser, error) {
	switch ct {
	case CompressionTypeNone:
		return io.NopCloser(src), nil
	case CompressionTypeGzip:
		return gzip.NewReader(src)
	case CompressionTypeZstd:
		zr, err := zstd.NewReader(src)
		if err != nil {
			return nil, err
		}
		return zr.IOReadCloser(), nil
	default:
		return nil, errors.New("unknown compression type")
	}
}

// nopWriteCloser implements [io.WriteCloser] with a no-op closer.
type nopWriteCloser struct {
	io.Writer
}

// Close implements [io.Closer].
func (nopWriteCloser) Close() error {
	return nil
}
