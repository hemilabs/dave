// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	dockerImageNginx = "nginx:1.27.5-alpine3.21-slim@sha256:b947b2630c97622793113555e13332eec85bdc7a0ac6ab697159af78942bb856"
	dockerImageMinio = "minio/minio:RELEASE.2025-03-12T18-04-18Z@sha256:46b3009bf7041eefbd90bd0d2b38c6ddc24d20a35d609551a1802c558c1c958f"
)

func testDocker(t *testing.T) bool {
	t.Helper()
	if e := os.Getenv("DAVE_DOCKER_TESTS"); e == "" || e == "0" {
		t.Skip("Skipping Docker tests - DAVE_DOCKER_TESTS disabled or not set")
		return false
	}
	return true
}

func TestSnapshotPipeline(t *testing.T) {
	t.Parallel()
	if !testDocker(t) {
		return
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// Snapshot test http server
	hbs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) > 1 {
			_, err := strconv.Atoi(r.URL.Path[1:])
			if err != nil {
				t.Errorf("Invalid heartbeat path: %v", err)
				return
			}
		}
		t.Logf("Received heartbeat at: %v", r.Host+r.URL.Path)
	}))
	defer hbs.Close()

	s3Url, _ := createMinIOContainer(t)
	_, nginxC := createNginxContainer(t)

	testFile := "testdatabase.tar.gz"
	dirToExtract := filepath.Join("testdata/", testFile)
	dirToBackup := t.TempDir()

	if err := extract(dirToExtract, dirToBackup); err != nil {
		t.Fatal(err)
	}

	dataDirs := []string{dirToBackup}
	// Create S3 repository
	s3, err := NewS3Repository(ctx, s3Url)
	if err != nil {
		t.Fatalf("new S3 backend: %v", err)
	}

	d, err := NewDave(s3, testDefaultConfig(t))
	if err != nil {
		t.Fatal(err)
	}

	// TODO: test that the container was actually stopped.

	var opts SnapshotOptions
	opts.ContainerID = nginxC.GetContainerID()
	opts.HeartbeatURL = hbs.URL
	_, err = d.Snapshot(ctx, opts, dataDirs)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotFreezeContainers(t *testing.T) {
	t.Parallel()
	if !testDocker(t) {
		return
	}

	tests := []struct {
		name       string
		numFreeze  int
		numRunning int
		viaCLI     bool
	}{
		{name: "no freeze containers", numFreeze: 0},
		{name: "one freeze container", numFreeze: 1},
		{name: "two freeze containers", numFreeze: 2},
		{name: "one freeze container, one other running container", numFreeze: 1, numRunning: 1},
		{name: "cli: no freeze containers", numFreeze: 0, viaCLI: true},
		{name: "cli: one freeze container", numFreeze: 1, viaCLI: true},
		{name: "cli: two freeze containers", numFreeze: 2, viaCLI: true},
		{name: "cli: one freeze container, one other running container", numFreeze: 1, numRunning: 1, viaCLI: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()

			repo, err := NewLocalRepository(t.Context(), t.TempDir())
			if err != nil {
				t.Fatalf("new local repository: %v", err)
			}
			d, err := NewDave(repo, testDefaultConfig(t))
			if err != nil {
				t.Fatal(err)
			}

			freezeIDs := make([]string, tt.numFreeze)
			freezeStartedAtBefore := make([]time.Time, tt.numFreeze)
			for i := range freezeIDs {
				_, c := createNginxContainer(t)
				freezeIDs[i] = c.GetContainerID()
				freezeStartedAtBefore[i] = containerStartedAt(t, ctx, d, freezeIDs[i])
			}

			// runningIDs are containers that are NOT passed via FreezeContainerIDs,
			// and must remain running (never stopped) throughout the snapshot.
			runningIDs := make([]string, tt.numRunning)
			runningStartedAtBefore := make([]time.Time, tt.numRunning)
			for i := range runningIDs {
				_, c := createNginxContainer(t)
				runningIDs[i] = c.GetContainerID()
				runningStartedAtBefore[i] = containerStartedAt(t, ctx, d, runningIDs[i])
			}

			dirToBackup := t.TempDir()
			if err := extract(filepath.Join("testdata", "testdatabase.tar.gz"), dirToBackup); err != nil {
				t.Fatal(err)
			}

			var snapshotTime time.Time
			if tt.viaCLI {
				// Drive the backup through the `dave backup` CLI command,
				// run as a subprocess, rather than calling Dave.Snapshot
				// directly. d (and its Docker client) is still used below
				// to inspect container state before and after.
				args := []string{"backup", "--repo", "local:" + t.TempDir(), "--json"}
				if len(freezeIDs) > 0 {
					args = append(args, "--freeze-container-ids", strings.Join(freezeIDs, ","))
				}
				args = append(args, dirToBackup)

				snapshotTime = time.Now()

				_, stderr, err := runDaveCLI(t, ctx, t.TempDir(), args...)
				if err != nil {
					t.Fatalf("dave backup: %v\nstderr: %s", err, stderr)
				}
			} else {
				opts := DefaultSnapshotOptions()
				opts.FreezeContainerIDs = freezeIDs
				snapshot, err := d.Snapshot(ctx, opts, []string{dirToBackup})
				if err != nil {
					t.Fatalf("snapshot: %v", err)
				}
				snapshotTime = snapshot.Time
			}

			for i, id := range freezeIDs {
				startedAtAfter := containerStartedAt(t, ctx, d, id)
				if !startedAtAfter.After(freezeStartedAtBefore[i]) {
					t.Fatalf("container %s: StartedAt did not advance (before=%s, after=%s); container was not stopped and restarted",
						id, freezeStartedAtBefore[i], startedAtAfter)
				}
				if !startedAtAfter.After(snapshotTime) {
					t.Fatalf("container %s: StartedAt (%s) is not after backup creation time (%s); container was restarted before the backup ran",
						id, startedAtAfter, snapshotTime)
				}
			}

			for i, id := range runningIDs {
				startedAtAfter := containerStartedAt(t, ctx, d, id)
				if !startedAtAfter.Equal(runningStartedAtBefore[i]) {
					t.Fatalf("container %s: StartedAt changed (before=%s, after=%s); container should not have been stopped",
						id, runningStartedAtBefore[i], startedAtAfter)
				}
			}
		})
	}
}

func TestSnapshotHealthcheck(t *testing.T) {
	t.Parallel()
	if !testDocker(t) {
		return
	}

	// healthcheckTimeout bounds the "never becomes healthy" cases to a very
	// short window, so Snapshot's own polling loop (dave.go) reliably
	// returns hCtx.Err() (context deadline exceeded) on its own, rather than
	// the test relying on an expiring test/CLI context.
	const healthcheckTimeout = 2 * time.Second

	type tc struct {
		name               string
		setup              func(t *testing.T) [][]string // returns one or more healthcheck specs, each e.g. []string{"url", srv.URL}
		wantErr            bool                          // expect the backup to fail
		wantTimeout        bool                          // unhealthy case: expect the failure to be a context-deadline-exceeded timeout
		healthcheckTimeout time.Duration                 // set for "unhealthy forever" cases
		viaCLI             bool
	}

	// Every case below is run both directly through Dave.Snapshot and through
	// the `dave backup` CLI subprocess (the "cli: "-prefixed duplicate with
	// viaCLI: true), mirroring TestSnapshotFreezeContainers.
	tests := []tc{
		{
			name: "synctest: tips match",
			setup: func(t *testing.T) [][]string {
				controlSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)
				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
		},
		{
			name: "synctest: tips do not match",
			setup: func(t *testing.T) [][]string {
				controlSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xdef", false)
				t.Cleanup(expSrv.Close)
				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
			wantErr:            true,
			wantTimeout:        true,
			healthcheckTimeout: healthcheckTimeout,
		},
		{
			name: "synctest: fails once then succeeds",
			setup: func(t *testing.T) [][]string {
				var calls atomic.Int32
				controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hash := "0xabc"
					if calls.Add(1) == 1 {
						hash = "0xdef" // mismatched on the first poll
					}

					res := ethBlockByNumberResponse{}
					res.Result.Hash = hash

					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(res); err != nil {
						t.Errorf("could not encode response: %v", err)
					}
				}))
				t.Cleanup(controlSrv.Close)

				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)

				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
		},
		{
			name: "synctest: controlUrl returns an error",
			setup: func(t *testing.T) [][]string {
				controlSrv := newBlockServer(t, "", true)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)
				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
			wantErr: true,
		},
		{
			name: "synctest: experimentalUrl returns an error",
			setup: func(t *testing.T) [][]string {
				controlSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "", true)
				t.Cleanup(expSrv.Close)
				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
			wantErr: true,
		},
		{
			name: "synctest: incorrect number of args",
			setup: func(t *testing.T) [][]string {
				return [][]string{{"synctest", "http://control.invalid"}} // missing experimental URL
			},
			wantErr: true,
		},
		{
			name: "url: 200 is healthy",
			setup: func(t *testing.T) [][]string {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(srv.Close)
				return [][]string{{"url", srv.URL}}
			},
		},
		{
			name: "url: fails once then succeeds",
			setup: func(t *testing.T) [][]string {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == 1 {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(srv.Close)
				return [][]string{{"url", srv.URL}}
			},
		},
		{
			name: "url: non-200 is unhealthy",
			setup: func(t *testing.T) [][]string {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				t.Cleanup(srv.Close)
				return [][]string{{"url", srv.URL}}
			},
			wantErr:            true,
			wantTimeout:        true,
			healthcheckTimeout: healthcheckTimeout,
		},
		{
			name: "url: incorrect number of args",
			setup: func(t *testing.T) [][]string {
				return [][]string{{"url"}} // missing required URL arg
			},
			wantErr: true,
		},
		{
			name: "multiple healthchecks: both succeed",
			setup: func(t *testing.T) [][]string {
				urlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(urlSrv.Close)

				controlSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)

				return [][]string{
					{"url", urlSrv.URL},
					{"synctest", controlSrv.URL, expSrv.URL},
				}
			},
		},
		{
			name: "multiple healthchecks: one fails, one succeeds",
			setup: func(t *testing.T) [][]string {
				urlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(urlSrv.Close)

				controlSrv := newBlockServer(t, "", true)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)

				return [][]string{
					{"url", urlSrv.URL},
					{"synctest", controlSrv.URL, expSrv.URL},
				}
			},
			wantErr: true,
		},
		{
			name: "cli: synctest: tips match",
			setup: func(t *testing.T) [][]string {
				controlSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)
				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
			viaCLI: true,
		},
		{
			name: "cli: synctest: tips do not match",
			setup: func(t *testing.T) [][]string {
				controlSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xdef", false)
				t.Cleanup(expSrv.Close)
				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
			wantErr:            true,
			wantTimeout:        true,
			healthcheckTimeout: healthcheckTimeout,
			viaCLI:             true,
		},
		{
			name: "cli: synctest: fails once then succeeds",
			setup: func(t *testing.T) [][]string {
				var calls atomic.Int32
				controlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hash := "0xabc"
					if calls.Add(1) == 1 {
						hash = "0xdef" // mismatched on the first poll
					}

					res := ethBlockByNumberResponse{}
					res.Result.Hash = hash

					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(res); err != nil {
						t.Errorf("could not encode response: %v", err)
					}
				}))
				t.Cleanup(controlSrv.Close)

				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)

				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
			viaCLI: true,
		},
		{
			name: "cli: synctest: controlUrl returns an error",
			setup: func(t *testing.T) [][]string {
				controlSrv := newBlockServer(t, "", true)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)
				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
			wantErr: true,
			viaCLI:  true,
		},
		{
			name: "cli: synctest: experimentalUrl returns an error",
			setup: func(t *testing.T) [][]string {
				controlSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "", true)
				t.Cleanup(expSrv.Close)
				return [][]string{{"synctest", controlSrv.URL, expSrv.URL}}
			},
			wantErr: true,
			viaCLI:  true,
		},
		{
			name: "cli: synctest: incorrect number of args",
			setup: func(t *testing.T) [][]string {
				return [][]string{{"synctest", "http://control.invalid"}} // missing experimental URL
			},
			wantErr: true,
			viaCLI:  true,
		},
		{
			name: "cli: url: 200 is healthy",
			setup: func(t *testing.T) [][]string {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(srv.Close)
				return [][]string{{"url", srv.URL}}
			},
			viaCLI: true,
		},
		{
			name: "cli: url: fails once then succeeds",
			setup: func(t *testing.T) [][]string {
				var calls atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == 1 {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(srv.Close)
				return [][]string{{"url", srv.URL}}
			},
			viaCLI: true,
		},
		{
			name: "cli: url: non-200 is unhealthy",
			setup: func(t *testing.T) [][]string {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				t.Cleanup(srv.Close)
				return [][]string{{"url", srv.URL}}
			},
			wantErr:            true,
			wantTimeout:        true,
			healthcheckTimeout: healthcheckTimeout,
			viaCLI:             true,
		},
		{
			name: "cli: url: incorrect number of args",
			setup: func(t *testing.T) [][]string {
				return [][]string{{"url"}} // missing required URL arg
			},
			wantErr: true,
			viaCLI:  true,
		},
		{
			name: "cli: multiple healthchecks: both succeed",
			setup: func(t *testing.T) [][]string {
				urlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(urlSrv.Close)

				controlSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)

				return [][]string{
					{"url", urlSrv.URL},
					{"synctest", controlSrv.URL, expSrv.URL},
				}
			},
			viaCLI: true,
		},
		{
			name: "cli: multiple healthchecks: one fails, one succeeds",
			setup: func(t *testing.T) [][]string {
				urlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				t.Cleanup(urlSrv.Close)

				controlSrv := newBlockServer(t, "", true)
				t.Cleanup(controlSrv.Close)
				expSrv := newBlockServer(t, "0xabc", false)
				t.Cleanup(expSrv.Close)

				return [][]string{
					{"url", urlSrv.URL},
					{"synctest", controlSrv.URL, expSrv.URL},
				}
			},
			wantErr: true,
			viaCLI:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Generous outer timeout: every case, including the bounded
			// ones, is expected to resolve on its own well within this.
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()

			healthchecks := tt.setup(t)

			_, nginxC := createNginxContainer(t)

			dirToBackup := t.TempDir()
			if err := extract(filepath.Join("testdata", "testdatabase.tar.gz"), dirToBackup); err != nil {
				t.Fatal(err)
			}

			if tt.viaCLI {
				args := []string{
					"backup",
					"--repo", "local:" + t.TempDir(),
					"--container-id", nginxC.GetContainerID(),
				}
				for _, hc := range healthchecks {
					hcJSON, err := json.Marshal(hc)
					if err != nil {
						t.Fatalf("marshal healthcheck args: %v", err)
					}
					args = append(args, "--healthcheck", string(hcJSON))
				}
				args = append(args, "--json")
				if tt.healthcheckTimeout != 0 {
					args = append(args, "--healthcheck-timeout", tt.healthcheckTimeout.String())
				}
				args = append(args, dirToBackup)

				_, stderr, err := runDaveCLI(t, ctx, t.TempDir(), args...)
				if tt.wantErr {
					if err == nil {
						t.Fatalf("dave backup: expected error, got none\nstderr: %s", stderr)
					}
					if tt.wantTimeout && !strings.Contains(stderr, context.DeadlineExceeded.Error()) {
						t.Fatalf("dave backup: expected a %q timeout error\nstderr: %s", context.DeadlineExceeded, stderr)
					}
					return
				}
				if err != nil {
					t.Fatalf("dave backup: %v\nstderr: %s", err, stderr)
				}
				return
			}

			repo, err := NewLocalRepository(t.Context(), t.TempDir())
			if err != nil {
				t.Fatalf("new local repository: %v", err)
			}
			d, err := NewDave(repo, testDefaultConfig(t))
			if err != nil {
				t.Fatal(err)
			}

			opts := DefaultSnapshotOptions()
			opts.ContainerID = nginxC.GetContainerID()
			opts.Healthchecks = healthchecks
			if tt.healthcheckTimeout != 0 {
				opts.HealthcheckTimeout = tt.healthcheckTimeout
			}

			_, err = d.Snapshot(ctx, opts, []string{dirToBackup})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Snapshot: expected error, got none")
				}
				if tt.wantTimeout && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Snapshot: expected a %q timeout error, got: %v", context.DeadlineExceeded, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
		})
	}
}

// daveCLIBinary builds the dave CLI binary once, shared by every subtest
// that exercises it as a subprocess, and returns its path.
var daveCLIBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "dave-cli-test-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "dave")
	wd, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("could not get workding dir, this should not happen: %s", err))
	}

	out, err := exec.Command("go", "build", "-o", bin, fmt.Sprintf("%s/../../cmd/dave", wd)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("build dave binary: %w: %s", err, out)
	}
	return bin, nil
})

// runDaveCLI runs the built dave binary as a subprocess with the given
// arguments, using daveHome as its DAVE_HOME directory.
func runDaveCLI(t *testing.T, ctx context.Context, daveHome string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	bin, err := daveCLIBinary()
	if err != nil {
		t.Fatalf("build dave binary: %v", err)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "DAVE_HOME="+daveHome)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// containerStartedAt inspects the given container, asserts that it is
// currently running, and returns its State.StartedAt timestamp.
func containerStartedAt(t *testing.T, ctx context.Context, d *Dave, id string) time.Time {
	t.Helper()
	resp, err := d.dockerClient.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatalf("inspect container %s: %v", id, err)
	}
	if resp.State.Status != container.StateRunning {
		t.Fatalf("container %s: status = %s, want running", id, resp.State.Status)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, resp.State.StartedAt)
	if err != nil {
		t.Fatalf("parse StartedAt %q for container %s: %v", resp.State.StartedAt, id, err)
	}
	return startedAt
}

func TestS3Repository(t *testing.T) {
	t.Parallel()
	if !testDocker(t) {
		return
	}

	ctx := t.Context()
	const numSnapshots = 2

	s3Url, _ := createMinIOContainer(t)

	// Create S3 repository
	s3, err := NewS3Repository(ctx, s3Url)
	if err != nil {
		t.Fatalf("new S3 backend: %v", err)
	}

	// Add snapshots to S3.
	snapshots := make([]*Snapshot, numSnapshots)
	for i := numSnapshots - 1; i >= 0; i-- {
		snapshot := testSnapshotWithArchives(3)
		if err = s3.SnapshotAdd(ctx, snapshot); err != nil {
			t.Fatalf("add snapshot %s/%d: %v", snapshot.ID, i, err)
		}
		snapshots[i] = snapshot
	}

	// Test listing snapshots.
	ls, err := s3.SnapshotList(ctx)
	if err != nil {
		t.Errorf("list snapshots: %v", err)
	}
	if len(ls) != numSnapshots {
		t.Fatalf("want %d snapshots, got %d", numSnapshots, len(ls))
	}
	for i, s := range ls {
		if s.ID != snapshots[i].ID {
			t.Errorf("list snapshots: %d: want snapshot %s, got %s",
				i, s.ID, snapshots[i].ID)
		}
	}

	// Test retrieving a snapshot
	sn, err := s3.SnapshotByID(ctx, ls[0].ID)
	if err != nil {
		t.Errorf("get snapshot meta %s: %v", ls[0].ID, err)
	}
	if sn.ID != ls[0].ID {
		t.Errorf("snapshot meta id = %s, got %s", sn.ID, ls[0].ID)
	}

	// Test removing a snapshot
	if err = s3.SnapshotRemove(ctx, ls[0].ID); err != nil {
		t.Errorf("remove snapshot %s: %v", ls[0].ID, err)
	}
}

// createNginxContainer creates and starts a nginx Docker container, and waits
// until nginx is reachable.
func createNginxContainer(t *testing.T) (string, testcontainers.Container) {
	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        dockerImageNginx,
			ExposedPorts: []string{"80/tcp"},
			WaitingFor:   wait.ForHTTP("/").WithPort("80/tcp"),
		},
		Started: true,
	})
	t.Cleanup(func() {
		if err = c.Terminate(context.Background()); err != nil {
			t.Fatalf("terminate nginx container: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("create nginx container: %v", err)
	}

	port, err := c.MappedPort(t.Context(), "80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	host, err := c.Host(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	addr := net.JoinHostPort(host, port.Port())
	t.Logf("nginx container created at %s", addr)

	return fmt.Sprintf("http://%s", addr), c
}

// createMinIOContainer creates and starts a minio Docker container, and waits
// until minio reports itself as live before returning.
func createMinIOContainer(t *testing.T) (string, testcontainers.Container) {
	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        dockerImageMinio,
			ExposedPorts: []string{"9000/tcp"},
			WaitingFor:   wait.ForHTTP("/minio/health/live").WithPort("9000"),
			Cmd:          []string{"server", "/data"},
		},
		Started: true,
	})
	t.Cleanup(func() {
		if err = c.Terminate(context.Background()); err != nil {
			t.Fatalf("terminate minio container: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("create minio container: %v", err)
	}

	port, err := c.MappedPort(t.Context(), "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	host, err := c.Host(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	addr := net.JoinHostPort(host, port.Port())
	t.Logf("minio container created at %s", addr)

	const (
		bucketName = "test"
		cred       = "minioadmin"
	)
	client, err := minio.New(addr, &minio.Options{
		Creds: credentials.NewStaticV4(cred, cred, ""),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create bucket
	err = client.MakeBucket(t.Context(), bucketName, minio.MakeBucketOptions{})
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	url := fmt.Sprintf("http://%s:%s@%s:%s/%s", cred, cred,
		host, port.Port(), bucketName)
	return url, c
}

// newBlockServer returns a test HTTP server that behaves as an Ethereum
// JSON-RPC endpoint for eth_getBlockByNumber. If fail is true, the server
// responds with an error status instead of a block.
func newBlockServer(t *testing.T, hash string, fail bool) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		res := ethBlockByNumberResponse{}
		res.Result.Hash = hash

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(res); err != nil {
			t.Errorf("could not encode response: %v", err)
		}
	}))
}

type ethBlockByNumberResponse struct {
	Result struct {
		Hash string `json:"hash"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}
