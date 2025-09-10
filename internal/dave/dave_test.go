// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/h2non/gock"
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

func TestS3UploadRetries(t *testing.T) {
	type testTableItem struct {
		name            string
		retriesExpected uint
		uploadType      string
		backoff         time.Duration
		expectedError   error
	}

	testTable := []testTableItem{
		testTableItem{
			name:            "file upload with default retries",
			uploadType:      "file",
			retriesExpected: DefaultRetries,
			backoff:         1 * time.Millisecond,
		},
		testTableItem{
			name:            "archive upload with default retries",
			uploadType:      "archive",
			retriesExpected: DefaultRetries,
			backoff:         1 * time.Millisecond,
		},
		testTableItem{
			name:            "file upload with fewer than default retries",
			uploadType:      "file",
			retriesExpected: 3,
			backoff:         1 * time.Millisecond,
		},
		testTableItem{
			name:            "archive upload with fewer than default retries",
			uploadType:      "archive",
			retriesExpected: 3,
			backoff:         1 * time.Millisecond,
		},
		testTableItem{
			name:            "file upload with more than default retries",
			uploadType:      "file",
			retriesExpected: 6,
			backoff:         1 * time.Millisecond,
		},
		testTableItem{
			name:            "archive upload with more than default retries",
			uploadType:      "archive",
			retriesExpected: 6,
			backoff:         1 * time.Millisecond,
		},
		testTableItem{
			name:            "file upload with larger backoff",
			uploadType:      "file",
			retriesExpected: 2,
			backoff:         10 * time.Millisecond,
		},
		testTableItem{
			name:            "archive upload with with larger backoff",
			uploadType:      "archive",
			retriesExpected: 2,
			backoff:         10 * time.Millisecond,
		},
		testTableItem{
			name:            "error upon negative",
			uploadType:      "archive",
			retriesExpected: 2,
			backoff:         -10 * time.Millisecond,
			expectedError:   ErrNegativeBackoff,
		},
	}

	for _, tti := range testTable {
		t.Run(tti.name, func(t *testing.T) {
			defer gock.Off()

			s3Url := "http://myfakeserver.test:40404"

			t.Logf("the s3 url is %s", s3Url)

			repo, err := NewS3Repository(t.Context(), s3Url+"/test")
			if err != nil {
				t.Fatalf("could not set up new s3 repository: %s", err)
			}

			repo.SetMaxRetries(tti.retriesExpected)
			if err := repo.SetBackoff(tti.backoff); err != nil {
				if errors.Is(err, tti.expectedError) {
					t.Logf("received expected error")
					return
				}

				t.Fatalf("received unexpected err: %s", err)
			}

			s3Repo := repo.(*s3Repository)

			credCtx := s3Repo.minioClient.CredContext()
			gock.InterceptClient(credCtx.Client)

			gock.New(s3Url).
				Get("/test").
				MatchParam("location", "").
				Times(int(tti.retriesExpected)).
				Reply(500)

			var timesCalled uint = 0

			gock.Observe(func(req *http.Request, m gock.Mock) {
				if req.URL.String() == fmt.Sprintf("%s/test/?location=", s3Url) {
					timesCalled++
				}
			})

			tmpFile, err := os.CreateTemp(".", "my-*-file.txt")
			if err != nil {
				t.Fatalf("error creating file: %s", err)
			}

			defer os.Remove(tmpFile.Name())

			before := time.Now()

			if tti.uploadType == "file" {
				err = s3Repo.uploadFile(t.Context(), tmpFile.Name(), "somefakecontenttype", []byte{})
			} else if tti.uploadType == "archive" {
				err = s3Repo.uploadArchive(t.Context(), ".", &SnapshotArchive{
					path: fmt.Sprintf("./%s", tmpFile.Name()),
					Name: tmpFile.Name(),
				})
			} else {
				t.Fatalf("unexpected uploadType: %s", tti.uploadType)
			}

			after := time.Now()

			if errors.Is(err, gock.ErrCannotMatch) {
				t.Fatalf("gock should be able to match requests: %s", err)
			}

			if err == nil {
				t.Fatalf("expected an error updating the metadata")
			}

			if timesCalled != tti.retriesExpected {
				t.Fatalf("expected url to be called %d times, received %d", tti.retriesExpected, timesCalled)
			}

			expectedMinMilliseconds := int64(tti.backoff.Milliseconds())
			for range tti.retriesExpected - 1 {
				expectedMinMilliseconds *= expectedMinMilliseconds
			}

			if after.Sub(before).Milliseconds() < expectedMinMilliseconds {
				t.Fatalf("difference was less than expected: %d < %d", after.Sub(before).Milliseconds(), expectedMinMilliseconds)
			}

		})
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
