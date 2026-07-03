// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrSnapshotNotExist = errors.New("snapshot does not exist")

const (
	// metaFilename is the filename used for metadata files.
	metaFilename = "meta.json"

	// metaContentType is the Media Type used for metadata files.
	metaContentType = "application/json"
)

// Repository is a storage implementation which stores snapshots.
type Repository interface {
	Metadata(ctx context.Context) (*RepositoryMeta, error)
	MetadataUpdate(ctx context.Context, meta *RepositoryMeta) error

	SnapshotAdd(ctx context.Context, snapshot *Snapshot) error
	SnapshotRetrieve(ctx context.Context, id string, dest string) error
	SnapshotByID(ctx context.Context, id string) (*Snapshot, error)
	SnapshotList(ctx context.Context) ([]*Snapshot, error)
	SnapshotRemove(ctx context.Context, id string) error
}

// RepositoryMeta stores metadata associated with a repository.
type RepositoryMeta struct {
	Latest *Snapshot `json:"latest"`
}

// RepositoryCreator returns a repository. The given string is either a path
// or URI for the repository.
type RepositoryCreator func(ctx context.Context, s string) (Repository, error)

// repoBackends stores the available repository implementations.
var repoBackends = map[string]RepositoryCreator{
	"local": NewLocalRepository,
	"s3":    NewS3Repository,
}

// NewRepository returns a repository implementation from the given string.
// The string should be in the format 'backend:path', for example:
//
//	local:/path/to/storage/
//	s3:account-id.cloudflarestorage.com/bucketname
//	s3:https://s3.us-east-2.amazonaws.com/bucketname
func NewRepository(ctx context.Context, s string) (Repository, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		// TODO: could default to local, but risk it being accidental?
		return nil, fmt.Errorf("invalid repository string: %s", s)
	}

	newBackend, ok := repoBackends[parts[0]]
	if !ok {
		return nil, fmt.Errorf("invalid repository backend: %s", parts[0])
	}
	return newBackend(ctx, parts[1])
}
