// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// archiveHashes contains the hash algorithms which will be used to create
// checksums for each snapshot archive.
var archiveHashes = map[string]crypto.Hash{
	"sha256": crypto.SHA256,
	"sha512": crypto.SHA512,
}

// archive creates a tar archive of the given directory.
func (d *Dave) archive(ctx context.Context, name, dir, src string, compression CompressionType) (*SnapshotArchive, error) {
	archiveName := name + ".tar" + compression.fileExtension()
	snapArchive := &SnapshotArchive{
		Name:        archiveName,
		compression: compression,
		path:        filepath.Join(dir, archiveName),
	}

	start := time.Now()
	slog.Info("Creating archive",
		"name", snapArchive.Name, "dst", snapArchive.path, "src", src)

	// Create temporary file to write to.
	f, err := os.CreateTemp(dir, snapArchive.Name+".dave-tmp-")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	defer func(f *os.File) {
		if err != nil {
			// Close and remove file.
			// Nothing will happen if the file has already been renamed.
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}(f)

	// Tar archive directory.
	hw := newHashWriter(f)
	if err = d.tarDir(ctx, hw, src, compression); err != nil {
		return nil, fmt.Errorf("archive dir %s: %w", src, err)
	}

	// Close then rename file.
	if err = f.Close(); err != nil {
		return nil, fmt.Errorf("close archive file: %w", err)
	}
	if err = os.Rename(f.Name(), snapArchive.path); err != nil {
		return nil, fmt.Errorf("rename archive file: %w", err)
	}

	snapArchive.Checksums = hw.Hashes()
	snapArchive.Size = hw.Written()

	slog.Debug("Created archive",
		"name", snapArchive.Name, "dst", snapArchive.path, "src", src,
		"size", snapArchive.Size, "checksums", snapArchive.Checksums,
		"duration", time.Since(start))
	return snapArchive, nil
}

// tarDir creates a tar archive containing the files in the given src directory.
func (d *Dave) tarDir(ctx context.Context, w io.Writer, src string, compression CompressionType) error {
	// tar -> buffer -> compression -> w
	cw, err := newCompressionEncoder(compression, w)
	if err != nil {
		return fmt.Errorf("create compression encoder: %w", err)
	}
	bw := bufio.NewWriterSize(cw, 4096)
	tw := tar.NewWriter(bw)

	dir := filepath.Dir(src)
	totalSize, err := CalculateDirSize(dir)
	if err != nil {
		return err
	}
	p := NewProgressBar(ctx, totalSize)
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// TODO: excludes!

		// Read file info.
		info, err := d.Info()
		if err != nil {
			return err
		}

		// Create file header.
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		// Create relative path.
		header.Name, err = filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(header.Name)

		// Write header.
		if err = tw.WriteHeader(header); err != nil {
			return err
		}

		// Skip directories and non-regular files.
		if d.IsDir() {
			return nil
		}

		// Write file contents to archive.
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		p.Update(int(info.Size()))
		return err
	})
	if err != nil {
		_ = tw.Close()
		_ = cw.Close()
		return fmt.Errorf("walk dir: %w", err)
	}

	// Flush and close writers.
	if err = tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err = bw.Flush(); err != nil {
		return fmt.Errorf("flush buffered writer: %w", err)
	}
	if err = cw.Close(); err != nil {
		return fmt.Errorf("close compression writer: %w", err)
	}
	return nil
}

// hashWriter hashes data as it is being written to the writer.
// It is very similar to io.MultiWriter, however it stores the hash writers
// to return the hashes from.
type hashWriter struct {
	writers []io.Writer
	written uint64
	hashes  map[string]hash.Hash
}

func newHashWriter(w io.Writer) *hashWriter {
	cw := &hashWriter{
		writers: make([]io.Writer, 0, len(archiveHashes)+1),
		hashes:  make(map[string]hash.Hash),
	}
	// Add the output writer to the writers.
	cw.writers = append(cw.writers, w)

	// Initialise each hash and add it to writers.
	for n, h := range archiveHashes {
		hf := h.New()
		cw.writers = append(cw.writers, hf)
		cw.hashes[n] = hf
	}
	return cw
}

// Write implements [io.Writer].
func (hw *hashWriter) Write(p []byte) (n int, err error) {
	s := len(p)
	// Same as implementation of Write for io.multiWriter.
	for _, w := range hw.writers {
		n, err = w.Write(p)
		if err != nil {
			return
		}
		if n != s {
			err = io.ErrShortWrite
			return
		}
	}
	hw.written += uint64(s)
	return s, nil
}

// Written returns the total number of bytes written.
func (hw *hashWriter) Written() uint64 {
	return hw.written
}

// Hashes returns a map of each hash name and the hexadecimal encoded hash.
func (hw *hashWriter) Hashes() map[string]string {
	m := make(map[string]string, len(hw.hashes))
	for name, h := range hw.hashes {
		m[name] = hex.EncodeToString(h.Sum(nil))
	}
	return m
}
