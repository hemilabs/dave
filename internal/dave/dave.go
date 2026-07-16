// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"golang.org/x/sync/errgroup"

	"github.com/hemilabs/dave/internal/healthcheck"
)

type HeartbeatStatus int

const (
	HeartbeatStatusOK     HeartbeatStatus = 0
	HeartbeatStatusFailed HeartbeatStatus = 1
)

type FileType int

const (
	FileTypeData FileType = iota + 1
	FileTypeArchive
)

var fileTypePaths = map[FileType]string{
	FileTypeData:    "data",
	FileTypeArchive: "archive",
}

// Dave is a backup and archival tool.
type Dave struct {
	daveDir string
	repo    Repository

	rsyncPath    string
	httpClient   *http.Client
	dockerClient *dockerclient.Client
}

// Config is the Dave configuration.
type Config struct {
	// HomeDir is Dave's home directory. It is used to store Dave's files.
	HomeDir string
}

func DefaultConfig() *Config {
	if testing.Testing() {
		// Catch bugs in tests failing to use the test default config.
		// We don't want to use the actual home directory in tests.
		panic("bug: DefaultConfig should not be called in tests")
	}

	daveDir := os.Getenv("DAVE_HOME")
	if daveDir == "" {
		homeDir, _ := os.UserHomeDir()
		daveDir = filepath.Join(homeDir, ".dave")
	}
	return &Config{
		HomeDir: daveDir,
	}
}

// NewDave returns a new Dave.
func NewDave(repo Repository, opts *Config) (*Dave, error) {
	if repo == nil {
		return nil, errors.New("repository is required")
	}
	if opts == nil {
		opts = DefaultConfig()
	}

	// Setup Docker client.
	dockerClient, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("new Docker client: %w", err)
	}

	return &Dave{
		daveDir:      opts.HomeDir,
		repo:         repo,
		httpClient:   http.DefaultClient,
		dockerClient: dockerClient,
	}, nil
}

// SnapshotOptions are the options for creating a snapshot.
type SnapshotOptions struct {
	ContainerID        string
	HeartbeatURL       string
	CompressionType    CompressionType
	KeepArchives       bool
	FreezeContainerIDs []string
	Healthchecks       [][]string
	HealthcheckTimeout time.Duration
}

// DefaultSnapshotOptions returns the default SnapshotOptions.
func DefaultSnapshotOptions() SnapshotOptions {
	return SnapshotOptions{
		CompressionType:    CompressionTypeGzip,
		HealthcheckTimeout: 30 * time.Minute,
	}
}

// Snapshot creates a new snapshot.
func (d *Dave) Snapshot(ctx context.Context, opts SnapshotOptions, dataDirs []string) (snapshot *Snapshot, err error) {
	if opts.HeartbeatURL == "" {
		slog.Warn("Heartbeat URL is not set, no heartbeat will be sent")
	}
	defer func() {
		status := HeartbeatStatusOK
		if err != nil {
			status = HeartbeatStatusFailed
		}
		err = errors.Join(err, d.heartbeat(ctx, opts.HeartbeatURL, status))
	}()

	// Setup directories.
	if err = d.setupDirs(dataDirs); err != nil {
		return nil, fmt.Errorf("setup directories: %w", err)
	}

	// Find rsync binary
	if d.rsyncPath, err = d.findRsync(ctx); err != nil {
		return nil, fmt.Errorf("find rsync binary: %w", err)
	}

	// Create a new snapshot.
	snapshot = NewSnapshot()
	slog.Info("Creating snapshot", "id", snapshot.ID, "time", snapshot.Time)

	containersToStop := append([]string{opts.ContainerID}, opts.FreezeContainerIDs...)

	for _, containerID := range containersToStop {
		if containerID == "" {
			continue // opts.ContainerID may be an empty string
		}

		slog.Info("Stopping container", "container_id", containerID)
		if err = d.stopNode(ctx, containerID); err != nil {
			slog.Error("Failed to stop Docker container", "err", err)
			return nil, fmt.Errorf("stop node: %w", err)
		}
	}

	// 2. Clone the data directories.
	backupErr := d.backup(ctx, dataDirs)

	for _, containerID := range containersToStop {
		if containerID == "" {
			continue // opts.ContainerID may be an empty string
		}

		slog.Info("Starting node", "container_id", containerID)
		if err = d.startNode(ctx, containerID); err != nil {
			slog.Error("Failed to start Docker container", "err", err)
			return nil, fmt.Errorf("stop node: %w", err)
		}

		// TODO: add a way to healthcheck each container, including checks that
		// are context-specific (ex. does a restarted node keep up with a tip)
		// for now, just check that the containers are back up and running
		if err := d.waitForContainerRunning(ctx, containerID); err != nil {
			return nil, err
		}
	}

	if len(opts.Healthchecks) != 0 {
		const healthCheckFrequency = 1 * time.Second

		hErrg, hEgCtx := errgroup.WithContext(ctx)
		for _, hc := range opts.Healthchecks {
			hErrg.Go(func() error {
				hcCtx, hcCancel := context.WithTimeout(hEgCtx, opts.HealthcheckTimeout)
				defer hcCancel()

				for {
					ok, err := healthcheck.Perform(hcCtx, hc)
					if err != nil {
						return fmt.Errorf("error performing health check: %w", err)
					}
					if ok {
						return nil
					}

					select {
					case <-hcCtx.Done():
						return hcCtx.Err()
					case <-time.After(healthCheckFrequency):
					}
				}
			})
		}
		if err := hErrg.Wait(); err != nil {
			return nil, fmt.Errorf("error performing health check: %w", err)
		}
	}

	// If the backup failed, want to exit here.
	if backupErr != nil {
		slog.Error("Snapshot failed", "err", backupErr)
		return nil, fmt.Errorf("backup: %w", backupErr)
	}

	// Create a directory to store the archives for this snapshot.
	archivesPath := d.filePath(FileTypeArchive, snapshot.ID)
	if err = os.MkdirAll(archivesPath, 0o700); err != nil {
		return nil, fmt.Errorf("create archive dir: %w", err)
	}

	// Create dataDirs.
	var errg errgroup.Group
	var mx sync.Mutex
	for _, dir := range dataDirs {
		errg.Go(func() error {
			n := filepath.Base(dir)
			archive, err := d.archive(ctx, n, archivesPath,
				d.filePath(FileTypeData, n), opts.CompressionType)
			if err != nil {
				slog.Error("Failed to create archive",
					"name", n, "dir", dir, "err", err)
				return err
			}

			mx.Lock()
			snapshot.addArchive(archive)
			mx.Unlock()
			return nil
		})
	}
	if err = errg.Wait(); err != nil {
		return nil, fmt.Errorf("create snapshot dataDirs: %w", err)
	}

	defer func() { // Clean up.
		if opts.KeepArchives {
			slog.Info("Keeping local archives", "path", archivesPath)
			return
		}
		if rmErr := os.RemoveAll(archivesPath); rmErr != nil {
			// TODO: do we want this to be fatal?
			slog.Warn("Failed to remove local archives path", "err", rmErr)
		}
	}()

	// Add snapshot to repository.
	if err = d.repo.SnapshotAdd(ctx, snapshot); err != nil {
		return nil, fmt.Errorf("upload snapshot to s3: %w", err)
	}

	// Update repository metadata.
	meta := &RepositoryMeta{
		Latest: &Snapshot{
			ID:   snapshot.ID,
			Time: snapshot.Time,
		},
	}
	if err = d.repo.MetadataUpdate(ctx, meta); err != nil {
		return nil, fmt.Errorf("upload snapshot to s3: %w", err)
	}

	slog.Info("Snapshot successful!", "id", snapshot.ID, "time", snapshot.Time)
	slog.Info("\"Dave is a trustworthy guy\" - Marco Peereboom, March 24th, 2025.")
	return snapshot, nil
}

// SnapshotList returns stored snapshots.
func (d *Dave) SnapshotList(ctx context.Context) ([]*Snapshot, error) {
	return d.repo.SnapshotList(ctx)
}

func (d *Dave) SnapshotRemove(ctx context.Context, id string) error {
	return d.repo.SnapshotRemove(ctx, id)
}

func (d *Dave) ParallelSnapshotRemove(ctx context.Context, ids []string, report func(id string, err error) error) error {
	errg, ctx := errgroup.WithContext(ctx)
	for _, id := range ids {
		errg.Go(func() error {
			err := d.SnapshotRemove(ctx, id)
			if report != nil {
				err = report(id, err)
			}
			return err
		})
	}
	return errg.Wait()
}

// setupDirs sets up directories used when creating a snapshot, and validates
// the provided dataDirs.
func (d *Dave) setupDirs(dataDirs []string) (err error) {
	slog.Debug("Setting up directories", "dir", d.daveDir)
	for _, path := range fileTypePaths {
		if err = os.MkdirAll(filepath.Join(d.daveDir, path), 0o700); err != nil {
			return fmt.Errorf("create path: %w", err)
		}
	}

	// Make data directories absolute paths, also check they all exist.
	for i, dir := range dataDirs {
		// Create absolute path.
		if dir, err = filepath.Abs(dir); err != nil {
			return fmt.Errorf("filepath abs: %w", err)
		}
		dataDirs[i] = dir

		// Check the directory actually exists.
		if _, err = os.Lstat(dir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("data directory %q does not exist", dir)
			}
			return fmt.Errorf("lstat: %w", err)
		}
	}
	return nil
}

// backup clones each data directory.
func (d *Dave) backup(ctx context.Context, dataDirs []string) error {
	slog.Info("Creating node backup")

	errg, egctx := errgroup.WithContext(ctx)
	for _, dir := range dataDirs {
		errg.Go(func() (err error) {
			defer func() {
				// This should never panic, but just in case.
				if r := recover(); r != nil {
					err = errors.Join(err, fmt.Errorf("panic: %w", err))
				}
			}()

			err = d.rsync(egctx, dir, d.filePath(FileTypeData, filepath.Base(dir)))
			if err != nil {
				return fmt.Errorf("rsync: %w", err)
			}
			return nil
		})
	}
	return errg.Wait()
}

// stopNode stops the node Docker container with the given ID.
func (d *Dave) stopNode(ctx context.Context, containerID string) error {
	return d.dockerClient.ContainerStop(ctx, containerID, container.StopOptions{})
}

// startNode starts the node Docker container with the given ID.
func (d *Dave) startNode(ctx context.Context, containerID string) error {
	return d.dockerClient.ContainerStart(ctx, containerID, container.StartOptions{})
}

func (d *Dave) waitForContainerRunning(ctx context.Context, containerID string) error {
	for {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		default:
			if started, err := d.containerHasStarted(ctx, containerID); err != nil {
				return err
			} else if started {
				return nil
			}
		}
	}
}

func (d *Dave) containerHasStarted(ctx context.Context, containerID string) (bool, error) {
	resp, err := d.dockerClient.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, err
	}

	return resp.State.Status == container.StateRunning, nil
}

// findRsync finds a rsync binary and checks the version.
func (d *Dave) findRsync(ctx context.Context) (string, error) {
	path, err := exec.LookPath("rsync")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", exec.ErrNotFound
		}
		return "", fmt.Errorf("look up rsync path: %w", err)
	}

	buf, err := d.execCmd(ctx, path, "--version")
	if err != nil {
		return "", fmt.Errorf("execute '%s --version': %w", path, err)
	}
	output := buf.String()
	if !strings.Contains(output, "rsync") {
		// Something could be wrong.
		return "", fmt.Errorf("'%s --version': output does not contain 'rsync'", path)
	}

	slog.Debug("Found rsync binary", "path", path, "version", output)
	return path, nil
}

// rsync uses rsync to sync two directories.
func (d *Dave) rsync(ctx context.Context, fromDir, toDir string) error {
	slog.Info("Syncing directory", "from", fromDir, "to", toDir)
	start := time.Now()

	// Make sure fromDir has a trailing slash, which causes rsync to copy the
	// contents of fromDir into toDir, instead of creating a new directory
	// inside toDir.
	if fromDir[len(fromDir)-1] != '/' {
		fromDir += "/"
	}

	// Execute: rsync -a --delete <from> <to>
	// This essentially mirrors fromDir to toDir. The --delete flag causes files
	// which are removed in fromDir to also be removed in toDir.
	// TODO: discuss if newer rsync versions should be required in order to display
	// overall progress bar options '--info=progress2 --human-readable --no-inc-recursive'
	if buf, err := d.execCmd(ctx, d.rsyncPath, "-a", "--delete", fromDir, toDir); err != nil {
		slog.Error("Failed to rsync directory",
			"from", fromDir, "to", toDir, "err", err)
		if _, cerr := io.Copy(os.Stderr, buf); cerr != nil {
			slog.Error("Failed to copy rsync output", "err", cerr)
		}
		return fmt.Errorf("exec: %w", err)
	}
	slog.Debug("Syncing directory completed",
		"from", fromDir, "to", toDir, "duration", time.Since(start))
	return nil
}

// heartbeat sends an HTTP POST request to publish Dave's status to an external
// monitoring system. This allows alerts to be triggered when Dave fails.
func (d *Dave) heartbeat(ctx context.Context, heartbeatURL string, status HeartbeatStatus) error {
	if heartbeatURL == "" {
		// Heartbeat not enabled.
		return nil
	}

	uri := heartbeatURL
	if status != HeartbeatStatusOK {
		var err error
		uri, err = url.JoinPath(uri, "/"+strconv.Itoa(int(status)))
		if err != nil {
			return fmt.Errorf("create heartbeat url: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, http.NoBody)
	if err != nil {
		return fmt.Errorf("create heartbeat request: %w", err)
	}

	slog.Info("Sending heartbeat", "status", status)
	res, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send heartbeat request: %w", err)
	}
	_ = res.Body.Close()
	return nil
}

// execCmd executes a command. If an error occurs, the stderr/stdout of the
// executed command will be written to stderr.
func (d *Dave) execCmd(ctx context.Context, name string, args ...string) (*bytes.Buffer, error) {
	slog.Debug("Executing command", "name", name, "args", strings.Join(args, " "))
	buf := bytes.NewBuffer(nil)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = buf, buf
	defer func() {
		slog.Debug("Executed command", "code", cmd.ProcessState.ExitCode(),
			"name", name, "args", strings.Join(args, " "))
	}()
	if err := cmd.Run(); err != nil {
		return buf, fmt.Errorf("%s: %w", name, err)
	}
	return buf, nil
}

// filePath returns the path where a file should be stored.
func (d *Dave) filePath(fileType FileType, name string) string {
	return filepath.Join(d.daveDir, fileTypePaths[fileType], name)
}
