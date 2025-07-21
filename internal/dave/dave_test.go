// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// testDBArchive is the path to the test database archive.
const testDBArchive = "testdata/testdatabase.tar.gz"

// testDBHashes is the checksums for the test database archive.
var testDBHashes = map[string]string{
	"sha256": "da525a5d4b04830dcc4f091ae84699dfebdd9e246989246473bb5ef196fa37fa",
	"sha512": "d714f485536184aba619d43a9a7382d58c466d94485a867a5e0361b451b70c0172acc32ee2ec83b9249ff583ae96114581bffe544cf11d3f3955672b7be3dd3e",
}

func TestHeartbeat(t *testing.T) {
	t.Parallel()

	send := map[HeartbeatStatus]int{
		HeartbeatStatusOK:     1,
		HeartbeatStatusFailed: 1,
	}
	rcv := make(map[HeartbeatStatus]int)

	// Snapshot test http server
	hbs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := HeartbeatStatusOK
		if len(r.URL.Path) > 1 {
			i, err := strconv.Atoi(r.URL.Path[1:])
			if err != nil {
				t.Errorf("Invalid heartbeat path: %v", err)
				return
			}
			status = HeartbeatStatus(i)
		}
		rcv[status]++
		t.Logf("Received heartbeat at: %v", r.Host+r.URL.Path)
	}))
	defer hbs.Close()

	// Perform heartbeats
	d := Dave{httpClient: http.DefaultClient}
	for status, count := range send {
		for range count {
			if err := d.heartbeat(t.Context(), hbs.URL, status); err != nil {
				t.Errorf("Failed to send heartbeat (%d): %v", status, err)
			}
		}
	}

	// Check requests
	for k, v := range rcv {
		if v != send[k] {
			t.Errorf("Wrong heartbeat result (%d): got %d, want %d", k, v, send[k])
		}
		delete(send, k)
	}
	for k, v := range send {
		t.Errorf("Did not send heartbeat (%d): %v", k, v)
	}
}

func TestBackup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	testFile := "testdatabase.tar.gz"
	dirToExtract := filepath.Join("testdata/", testFile)
	dirToBackup := t.TempDir()

	if err := extract(dirToExtract, filepath.Dir(dirToBackup)); err != nil {
		t.Fatal(err)
	}

	repo, err := NewLocalRepository(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("new local repository: %v", err)
	}
	d, err := NewDave(repo, testDefaultConfig(t))
	if err != nil {
		t.Fatalf("new dave: %v", err)
	}

	dataDirs := []string{dirToBackup}
	if err = d.setupDirs(dataDirs); err != nil {
		t.Fatalf("setup directories: %v", err)
	}

	if d.rsyncPath, err = d.findRsync(ctx); err != nil {
		t.Fatalf("find rsync: %v", err)
	}

	if err = d.backup(ctx, dataDirs); err != nil {
		t.Fatalf("backup: %v", err)
	}

	for _, dir := range dataDirs {
		name := filepath.Base(dir)
		if _, err = d.execCmd(ctx, "diff", dir, d.filePath(FileTypeData, name)); err != nil {
			t.Errorf("found diff between original and backup: %v", err)
		}
	}
}

func TestArchive(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	testFile := "testdatabase.tar.gz"
	dirToExtract := filepath.Join("testdata/", testFile)
	dirToBackup := t.TempDir()

	if err := extract(dirToExtract, dirToBackup); err != nil {
		t.Fatal(err)
	}

	dataDirs := []string{dirToBackup}
	repo, err := NewLocalRepository(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDave(repo, testDefaultConfig(t))
	if err != nil {
		t.Fatal(err)
	}

	archivesPath := d.filePath(FileTypeArchive, "someid")
	if err = os.MkdirAll(archivesPath, 0o700); err != nil {
		t.Fatal(fmt.Errorf("create archive dir: %w", err))
	}

	for _, dir := range dataDirs {
		name := filepath.Base(dir)
		archive, err := d.archive(ctx, name, archivesPath, dir, DefaultCompressionType)
		if err != nil {
			t.Fatal(err)
		}

		if err := extract(archive.path, archivesPath); err != nil {
			t.Fatal(err)
		}

		if _, err = d.execCmd(ctx, "diff", dir, filepath.Join(archivesPath, name)); err != nil {
			t.Errorf("found diff between original and backup: %v", err)
			t.Fail()
		}
	}
}

// testSnapshotWithArchives creates a test snapshot with a defined number of archives.
func testSnapshotWithArchives(archives int) *Snapshot {
	snapshot := NewSnapshot()
	for i := range archives {
		snapshot.addArchive(&SnapshotArchive{
			Name:      fmt.Sprintf("test-%d.tar.gz", i),
			Checksums: testDBHashes,
			Size:      1,
			path:      testDBArchive,
		})
	}
	return snapshot
}

func testDefaultConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		HomeDir: t.TempDir(),
	}
}
