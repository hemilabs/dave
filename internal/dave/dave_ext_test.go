// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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
