// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"hypera.dev/lib/slog/pretty"

	"github.com/hemilabs/dave/internal/dave"
)

const backupHelp = `Create a new backup of directories.

If a container ID is provided, the container will be stopped while the
directories are being cloned, and will be started again afterwards.

If the heartbeat URL is provided, an HTTP POST request will be made to
report the backup result.

Usage:
  dave backup [flags] [directory...]

Examples:
  # Wait for an HTTP healthcheck to return 200 OK before finishing the backup.
  dave backup --healthcheck '["url", "http://localhost:8080/health"]' /data

  # Wait for two Ethereum JSON-RPC nodes to report the same latest block hash.
  dave backup --healthcheck '["synctest", "http://control:8545", "http://experimental:8545"]' /data

  # --healthcheck may be repeated; all checks must pass before the backup succeeds.
  dave backup \
    --healthcheck '["url", "http://localhost:8080/health"]' \
    --healthcheck '["synctest", "http://control:8545", "http://experimental:8545"]' \
    --healthcheck-timeout 10m \
    /data

  # --backup-strategy may be repeated; format is "<name>:<endpoint>:<timestamp>".
  # <name> must currently be "ethsethead".
  dave backup --backup-strategy 'ethsethead:http://localhost:8545:1700000000' /data

Flags:`

func runBackup(ctx context.Context, args []string) (any, error) {
	var (
		ct                 string
		freezeContainerIDs []string
		backupStrategies   []string
		err                error
	)
	opts := dave.DefaultSnapshotOptions()

	flag := newFlagSet("backup", backupHelp)
	flag.StringArrayVar(&backupStrategies, "backup-strategy", nil,
		`backup strategy "<name>:<endpoint>:<timestamp>" (repeatable)`)
	flag.StringVar(&ct, "compression", opts.CompressionType.String(),
		"compression type (options: none, gzip, zstd)")
	flag.StringVarP(&opts.ContainerID, "container-id", "c", opts.ContainerID, "container ID")
	flag.StringSliceVar(&freezeContainerIDs, "freeze-container-ids", nil, "container IDs to freeze")
	// flag.StringArrayVarP(&excludes, "exclude", "e", nil, "exclude pattern")
	flag.StringVar(&opts.HeartbeatURL, "heartbeat", opts.HeartbeatURL, "heartbeat URL")
	flag.DurationVar(&opts.HealthcheckTimeout, "healthcheck-timeout", opts.HealthcheckTimeout, "healthcheck timeout")
	flag.BoolVar(&opts.KeepArchives, "keep-archives", false, "keep archives locally (debug use only)")

	if err = flagParse(flag, args); err != nil {
		return nil, err
	}

	strategies, err := parseBackupStrategies(backupStrategies)
	if err != nil {
		return nil, err
	}

	opts.FreezeContainerIDs = freezeContainerIDs

	for _, hc := range gopts.Healthcheck {
		var hcArgs []string
		if err = json.Unmarshal([]byte(hc), &hcArgs); err != nil {
			return nil, fmt.Errorf("parse healthcheck: %w", err)
		}
		opts.Healthchecks = append(opts.Healthchecks, hcArgs)
	}

	args = flag.Args()

	if len(args) < 1 {
		flag.Usage()
		return nil, errors.New("missing directories")
	}

	// TODO: Discuss UI and progress stuff
	slog.SetDefault(slog.New(pretty.NewHandler(os.Stderr, &pretty.Options{
		Level:        verbosityToLevel(gopts.Verbosity),
		AddSource:    true,
		DisableColor: true,
		SourceFormatter: func(buf *pretty.Buffer, src *slog.Source) {
			dir, file := filepath.Split(src.File)
			buf.AppendString(filepath.Join(filepath.Base(dir), file))
			buf.AppendByte(':')
			buf.AppendInt(int64(src.Line))
		},
	})))

	slog.Info("parsed backup strategies", "len(strategies)", len(strategies))

	// Parse compression type.
	if opts.CompressionType, err = dave.ParseCompressionType(ct); err != nil {
		return nil, err
	}

	// Create repository.
	repo, err := newRepo(ctx)
	if err != nil {
		return nil, err
	}
	d, err := dave.NewDave(repo, nil)
	if err != nil {
		return nil, err
	}

	sort.Slice(strategies, func(i, j int) bool {
		return strategies[i].Timestamp > strategies[j].Timestamp
	})

	// assert descending order (this should be a test)
	for i := range strategies {
		if i == 0 {
			continue
		}

		if strategies[i].Timestamp > strategies[i-1].Timestamp {
			panic("strategy order incorrect")
		}
	}

	for _, s := range strategies {
		opts.PrebackupConfigs = append(opts.PrebackupConfigs, dave.PrebackupConfig{
			Name:      s.Name,
			Timestamp: s.Timestamp,
			Endpoint:  s.Endpoint,
		})
	}

	if len(opts.PrebackupConfigs) == 0 {
		return d.Snapshot(ctx, opts, args)
	}

	// I do not like that we return "any"thing here, but it is what it is
	// at this point
	ret := []any{}

	for _, prebackupConfig := range opts.PrebackupConfigs {
		block, err := dave.GetFirstBlockBeforeTime(ctx, uint64(prebackupConfig.Timestamp), prebackupConfig.Endpoint)
		if err != nil {
			return nil, err
		}

		if err := dave.SetHeadToBlock(ctx, prebackupConfig.Endpoint, block); err != nil {
			return nil, err
		}

		v, err := d.Snapshot(ctx, opts, args)
		if err != nil {
			return nil, err
		}

		ret = append(ret, v)
	}

	return ret, nil
}
