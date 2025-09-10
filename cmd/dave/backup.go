// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"hypera.dev/lib/slog/pretty"

	"github.com/hemilabs/dave/internal/dave"
)

const backupHelp = `Create a new backup of directories.

If a container ID is provided, the container will be stopped while the
directories are being cloned, and will be started again afterwards.

If a healthcheck URL is provided, it will be polled to make sure the system
is healthy after the directories have been cloned.

If the heartbeat URL is provided, a HTTP HEAD request will be made to
report the backup result.

Usage:
  dave backup [flags] [directory...]

Flags:`

func runBackup(ctx context.Context, args []string) (any, error) {
	var (
		ct  string
		err error
	)
	opts := dave.DefaultSnapshotOptions()

	flag := newFlagSet("backup", backupHelp)
	flag.StringVar(&ct, "compression", opts.CompressionType.String(),
		"compression type (options: none, gzip, zstd)")
	flag.StringVarP(&opts.ContainerID, "container-id", "c", opts.ContainerID, "container ID")
	// flag.StringArrayVarP(&excludes, "exclude", "e", nil, "exclude pattern")
	flag.StringVar(&opts.HeartbeatURL, "heartbeat", opts.HeartbeatURL, "heartbeat URL")
	flag.StringVar(&opts.HealthURL, "health", opts.HealthURL, "healthcheck URL")
	flag.DurationVar(&opts.HealthTimeout, "health-timeout", opts.HealthTimeout, "health check timeout")
	flag.BoolVar(&opts.KeepArchives, "keep-archives", true, "keep archives locally (debug use only)")
	flag.UintVar(&opts.MaxRetries, "max-retries", dave.DefaultRetries, "the maximum number of times to try to upload snapshot upon failure")
	flag.DurationVar(&opts.Backoff, "backoff", dave.DefaultBackoff, "the duration to backoff between upload requests, this increases exponentially")

	if err = flagParse(flag, args); err != nil {
		return nil, err
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

	// Parse compression type.
	if opts.CompressionType, err = dave.ParseCompressionType(ct); err != nil {
		return nil, err
	}

	// Create repository.
	repo, err := newRepo(ctx)
	if err != nil {
		return nil, err
	}

	repo.SetMaxRetries(opts.MaxRetries)
	if err := repo.SetBackoff(opts.Backoff); err != nil {
		return nil, err
	}

	d, err := dave.NewDave(repo, nil)
	if err != nil {
		return nil, err
	}

	return d.Snapshot(ctx, opts, args)
}
