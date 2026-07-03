// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Snapshot represents a snapshot taken of a node at a certain point in time.
type Snapshot struct {
	ID       string             `json:"id"`
	Time     time.Time          `json:"time"`
	Archives []*SnapshotArchive `json:"archives,omitempty"`
}

// NewSnapshot creates a new empty snapshot.
func NewSnapshot() *Snapshot {
	return &Snapshot{
		ID:   genID(),
		Time: time.Now().UTC(),
	}
}

// addArchive adds an archive to the snapshot.
func (s *Snapshot) addArchive(archive *SnapshotArchive) {
	s.Archives = append(s.Archives, archive)
}

// SnapshotArchive is an archived directory included in a snapshot.
type SnapshotArchive struct {
	// Name is the name of the archive.
	Name string `json:"name"`

	// Checksums contains hashes of the archive.
	Checksums map[string]string `json:"checksums"`

	// Size is the archive size in bytes.
	Size uint64 `json:"size"`

	// Compression is the CompressionType used for the archive.
	Compression CompressionType `json:"compression"`

	// path is the path to the archive file.
	path string
}

// genID generates a random 16-byte hexadecimal ID.
func genID() string {
	// Note: rand.Read cannot return a non-nil error.
	// If rand.Read fails to read random data, it will call [runtime.fatal].
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
