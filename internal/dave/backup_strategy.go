// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// BackupStrategyEntry is a single --backup-strategy entry, in the form
// "<name>:<seconds-ago>" (e.g. "eth:3600").
type BackupStrategyEntry struct {
	// Name identifies which historical backup strategy to apply. Currently
	// the only supported name is "eth"; unsupported names are rejected at
	// execution time (logged and skipped), not at parse time.
	Name string

	// SecondsAgo is how many seconds into the past, relative to now, the
	// backup should be taken from.
	SecondsAgo uint64
}

// ParseBackupStrategyEntry parses a single --backup-strategy value. It must
// be in the form "<name>:<seconds-ago>" (e.g. "eth:3600" for a backup taken
// from one hour in the past).
func ParseBackupStrategyEntry(s string) (BackupStrategyEntry, error) {
	name, secondsAgoStr, ok := strings.Cut(s, ":")
	if !ok || name == "" {
		return BackupStrategyEntry{}, fmt.Errorf("invalid backup strategy entry %q: expected format \"<name>:<seconds-ago>\"", s)
	}
	secondsAgo, err := strconv.ParseUint(secondsAgoStr, 10, 64)
	if err != nil {
		return BackupStrategyEntry{}, fmt.Errorf("invalid backup strategy entry %q: %w", s, err)
	}
	return BackupStrategyEntry{Name: name, SecondsAgo: secondsAgo}, nil
}

// runBackupStrategy processes opts.BackupStrategy, if any: for each entry,
// it rewinds op-geth to the block closest to (now - entry.SecondsAgo) via
// debug_setHead, and stores a separate backup of dataDirs from that point in
// time. Entries are processed from most-recent to least-recent, so op-geth's
// head only ever moves backward between iterations.
func (d *Dave) runBackupStrategy(ctx context.Context, opts SnapshotOptions, dataDirs []string) error {
	if len(opts.BackupStrategy) == 0 {
		return nil
	}

	if opts.ContainerID == "" {
		return errors.New("--container-id is required when --backup-strategy is set")
	}
	if opts.OpGethRPCURL == "" {
		return errors.New("--op-geth-rpc-url is required when --backup-strategy is set")
	}

	entries := slices.Clone(opts.BackupStrategy)
	slices.SortFunc(entries, func(a, b BackupStrategyEntry) int {
		return cmp.Compare(a.SecondsAgo, b.SecondsAgo)
	})

	client := newOpGethClient(opts.OpGethRPCURL, d.httpClient)

	for _, entry := range entries {
		if entry.Name != "eth" {
			slog.Error("Unsupported backup strategy name, skipping entry",
				"name", entry.Name, "seconds_ago", entry.SecondsAgo)
			continue
		}

		if err := d.runBackupStrategyEntry(ctx, client, opts, dataDirs, entry); err != nil {
			return fmt.Errorf("backup strategy entry %s:%d: %w", entry.Name, entry.SecondsAgo, err)
		}
	}

	return nil
}

// runBackupStrategyEntry performs a single --backup-strategy entry: it finds
// the block closest to (now - entry.SecondsAgo), rewinds op-geth to it, and
// stores a new snapshot of dataDirs from that point.
func (d *Dave) runBackupStrategyEntry(ctx context.Context, client *opGethClient, opts SnapshotOptions, dataDirs []string, entry BackupStrategyEntry) (err error) {
	target := time.Now().Unix() - int64(entry.SecondsAgo)
	slog.Info("Processing backup strategy entry",
		"name", entry.Name, "seconds_ago", entry.SecondsAgo,
		"target_time", time.Unix(target, 0).UTC())

	// op-geth must be running for these RPC calls; it already is, either
	// because Snapshot() restarted it earlier, or because the previous
	// entry's iteration restarted it before returning.
	var blockNum, blockTs uint64
	if err = callWithRetry(ctx, func() error {
		var cerr error
		blockNum, blockTs, cerr = client.findClosestBlock(ctx, target)
		return cerr
	}); err != nil {
		return fmt.Errorf("find closest block: %w", err)
	}
	blockTime := time.Unix(int64(blockTs), 0).UTC()

	slog.Info("Setting op-geth head", "block", blockNum, "block_time", blockTime)
	if err = callWithRetry(ctx, func() error {
		return client.setHead(ctx, blockNum)
	}); err != nil {
		return fmt.Errorf("set head to block %d: %w", blockNum, err)
	}

	// From here on, op-geth needs to be stopped to safely copy its data
	// directories. If anything below fails, make a best-effort attempt to
	// restart it before returning, so we don't leave the node down on top
	// of failing.
	opGethRunning := true
	defer func() {
		if err != nil && !opGethRunning {
			slog.Error("Backup strategy entry failed, restarting op-geth", "err", err)
			if rerr := d.startNode(ctx, opts.ContainerID); rerr != nil {
				slog.Error("Failed to restart op-geth after error", "err", rerr)
			}
		}
	}()

	slog.Info("Stopping op-geth for historical backup", "container_id", opts.ContainerID)
	if err = d.stopNode(ctx, opts.ContainerID); err != nil {
		return fmt.Errorf("stop op-geth: %w", err)
	}
	opGethRunning = false

	// Copy the data directories, now reflecting the rewound chain state.
	if err = d.backup(ctx, dataDirs); err != nil {
		return fmt.Errorf("copy data directories: %w", err)
	}

	slog.Info("Starting op-geth", "container_id", opts.ContainerID)
	if err = d.startNode(ctx, opts.ContainerID); err != nil {
		return fmt.Errorf("start op-geth: %w", err)
	}
	opGethRunning = true

	if err = d.waitForContainerRunning(ctx, opts.ContainerID); err != nil {
		return fmt.Errorf("wait for op-geth to start: %w", err)
	}

	// Archive and upload the copied data as its own snapshot, named with
	// the historical block's timestamp.
	snapshot := NewSnapshot()
	archivesPath := d.filePath(FileTypeArchive, snapshot.ID)
	if err = os.MkdirAll(archivesPath, 0o700); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}
	defer func() {
		if opts.KeepArchives {
			slog.Info("Keeping local archives", "path", archivesPath)
			return
		}
		if rmErr := os.RemoveAll(archivesPath); rmErr != nil {
			slog.Warn("Failed to remove local archives path", "err", rmErr)
		}
	}()

	suffix := blockTime.Format(time.RFC3339)
	for _, dir := range dataDirs {
		base := filepath.Base(dir)
		name := base + "-" + suffix
		archive, aerr := d.archive(ctx, name, archivesPath,
			d.filePath(FileTypeData, base), opts.CompressionType)
		if aerr != nil {
			err = fmt.Errorf("create archive %s: %w", name, aerr)
			return err
		}
		snapshot.addArchive(archive)
	}

	if err = d.repo.SnapshotAdd(ctx, snapshot); err != nil {
		return fmt.Errorf("upload backup strategy snapshot: %w", err)
	}

	slog.Info("Backup strategy entry complete",
		"name", entry.Name, "seconds_ago", entry.SecondsAgo,
		"block", blockNum, "block_time", blockTime, "snapshot_id", snapshot.ID)
	return nil
}
