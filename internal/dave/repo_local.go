// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sync/errgroup"
)

// localRepository is a repository which stores data on the local filesystem.
type localRepository struct {
	path string
}

var _ Repository = (*localRepository)(nil)

// NewLocalRepository creates a repository which stores data on the local fs.
func NewLocalRepository(_ context.Context, dir string) (Repository, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("determine absolute path of %q: %w", dir, err)
	}

	// Check path exists and is a directory (and create if not exists).
	stat, err := os.Stat(abs)
	if err != nil || !stat.IsDir() {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat %q: %w", dir, err)
		}
		if err = os.MkdirAll(abs, 0o700); err != nil {
			return nil, fmt.Errorf("mkdir: %w", err)
		}
	}

	return &localRepository{path: abs}, nil
}

func (l *localRepository) Metadata(_ context.Context) (*RepositoryMeta, error) {
	f, err := os.Open(filepath.Join(l.path, metaFilename))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return new(RepositoryMeta), nil
		}
		return nil, err
	}
	defer f.Close()

	var meta RepositoryMeta
	if err = json.NewDecoder(f).Decode(&meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (l *localRepository) MetadataUpdate(_ context.Context, meta *RepositoryMeta) error {
	f, err := os.Create(filepath.Join(l.path, metaFilename))
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(meta)
}

func (l *localRepository) SnapshotAdd(ctx context.Context, snapshot *Snapshot) error {
	snapshotPath := filepath.Join(l.path, snapshot.ID)
	if err := os.MkdirAll(snapshotPath, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", snapshotPath, err)
	}

	// Copy snapshot archives.
	errg, ectx := errgroup.WithContext(ctx)
	for _, archive := range snapshot.Archives {
		errg.Go(func() error {
			return copyArchive(ectx, archive, filepath.Join(snapshotPath, archive.Name))
		})
	}
	if err := errg.Wait(); err != nil {
		return fmt.Errorf("add snapshot archive: %w", err)
	}

	// Write snapshot metadata file.
	f, err := os.Create(filepath.Join(snapshotPath, metaFilename))
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(snapshot)
}

func (l *localRepository) SnapshotByID(_ context.Context, id string) (*Snapshot, error) {
	f, err := os.Open(filepath.Join(l.path, id, metaFilename))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSnapshotNotExist
		}
		return nil, err
	}
	defer f.Close()

	var snapshot Snapshot
	if err = json.NewDecoder(f).Decode(&snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (l *localRepository) SnapshotList(ctx context.Context) ([]*Snapshot, error) {
	metaFiles, err := filepath.Glob(filepath.Join(l.path, "*", metaFilename))
	if err != nil {
		return nil, err
	}
	snapshots := make([]*Snapshot, len(metaFiles))
	for i, metaFile := range metaFiles {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		f, err := os.Open(metaFile)
		if err != nil {
			return nil, err
		}

		var snapshot Snapshot
		if err = json.NewDecoder(f).Decode(&snapshot); err != nil {
			_ = f.Close()
			return nil, err
		}
		_ = f.Close()
		snapshots[i] = &snapshot
	}

	// Sort newest to oldest.
	slices.SortStableFunc(snapshots, func(a, b *Snapshot) int {
		return b.Time.Compare(a.Time)
	})
	return snapshots, nil
}

func (l *localRepository) SnapshotRemove(ctx context.Context, id string) error {
	snapshot, err := l.SnapshotByID(ctx, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(l.path, snapshot.ID))
}

func (l *localRepository) SetMaxRetries(maxRetries uint) {
	// no-op
}

func (l *localRepository) SetBackoffMilliseconds(backoffMilliseconds uint) {
	// no-op
}

type readerFunc func(p []byte) (n int, err error)

func (rf readerFunc) Read(p []byte) (n int, err error) {
	return rf(p)
}

func copyArchive(ctx context.Context, archive *SnapshotArchive, filename string) error {
	// Open archive file.
	af, err := os.Open(archive.path)
	if err != nil {
		return fmt.Errorf("open snapshot archive: %w", err)
	}
	defer af.Close()

	// Read file stat for file size.
	stat, err := af.Stat()
	if err != nil {
		return fmt.Errorf("stat snapshot archive: %w", err)
	}
	fileSize := stat.Size()

	// Create temporary file to write to.
	f, err := os.CreateTemp(filepath.Dir(filename), filepath.Base(filename)+".dave-tmp-")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func(f *os.File) {
		if err != nil {
			// Close and remove file.
			// Nothing will happen if the file has already been renamed.
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}(f)

	// Copy data to destination file.
	copied, err := io.Copy(f, readerFunc(func(p []byte) (int, error) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
			return af.Read(p)
		}
	}))
	if err != nil {
		return fmt.Errorf("copy snapshot archive: %w", err)
	}
	if copied != fileSize {
		return fmt.Errorf("snapshot archive copied %d bytes, expected %d", copied, fileSize)
	}

	// Close then rename file.
	if err = f.Close(); err != nil {
		return fmt.Errorf("close archive file: %w", err)
	}
	if err = os.Rename(f.Name(), filename); err != nil {
		return fmt.Errorf("rename archive file: %w", err)
	}

	return nil
}
