// Copyright (c) 2025 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

func TestArchiveExclusions(t *testing.T) {
	t.Parallel()

	const entryName = "entry"
	content := []byte("deadbeef")

	tests := []struct {
		name     string
		setup    func(t *testing.T, dir string) error
		exclude  bool
		wantMode fs.FileMode
	}{
		{
			name: "file",
			setup: func(t *testing.T, dir string) error {
				return os.WriteFile(filepath.Join(dir, entryName), content, 0o644)
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, dir string) error {
				return os.Mkdir(filepath.Join(dir, entryName), 0o755)
			},
			wantMode: fs.ModeDir,
		},
		{
			name: "symlink",
			setup: func(t *testing.T, dir string) error {
				if err := os.WriteFile(filepath.Join(dir, "target"), content, 0o644); err != nil {
					return err
				}
				return os.Symlink("target", filepath.Join(dir, entryName))
			},
			wantMode: fs.ModeSymlink,
		},
		{
			name: "dangling symlink",
			setup: func(t *testing.T, dir string) error {
				return os.Symlink("fake", filepath.Join(dir, entryName))
			},
			exclude: true,
		},
		{
			name: "symlink outside archive",
			setup: func(t *testing.T, dir string) error {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, content, 0o644); err != nil {
					return err
				}
				return os.Symlink(target, filepath.Join(dir, entryName))
			},
			wantMode: fs.ModeSymlink,
		},
		{
			name: "socket",
			setup: func(t *testing.T, dir string) error {
				l, err := net.Listen("unix", filepath.Join(dir, entryName))
				if err != nil {
					return err
				}
				t.Cleanup(func() { _ = l.Close() })
				return nil
			},
			exclude: true,
		},
		{
			name: "named pipe",
			setup: func(t *testing.T, dir string) error {
				return syscall.Mkfifo(filepath.Join(dir, entryName), 0o600)
			},
			exclude: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			src := t.TempDir()
			if err := tt.setup(t, src); err != nil {
				t.Fatal(err)
			}

			d := &Dave{}
			archivesPath := t.TempDir()
			archive, err := d.archive(ctx, "archive", archivesPath, src, DefaultCompressionType)
			if err != nil {
				t.Fatal(err)
			}

			if err := extract(archive.path, archivesPath); err != nil {
				t.Fatal(err)
			}
			entryPath := filepath.Join(archivesPath, filepath.Base(src), entryName)

			info, err := os.Lstat(entryPath)
			if tt.exclude {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("expected %s to be excluded from archive", entryPath)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected %s in archive: %v", entryPath, err)
			}

			switch tt.wantMode {
			case fs.ModeDir:
				if !info.IsDir() {
					t.Errorf("want dir, got %s", info.Mode())
				}
			case fs.ModeSymlink:
				if info.Mode()&fs.ModeSymlink == 0 {
					t.Fatalf("want symlink, got %s", info.Mode())
				}
				if _, err := os.Stat(entryPath); err != nil {
					t.Errorf("symlink target missing: %v", err)
				}
			default:
				got, err := os.ReadFile(entryPath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, content) {
					t.Errorf("content: %s != %s", got, content)
				}
			}
		})
	}
}

func TestRepositoryRetrieve(t *testing.T) {
	t.Parallel()

	const archiveCount = 3

	dst := t.TempDir()
	snapshot := NewSnapshot()
	for i := range archiveCount {
		archive, err := addCustomTestArchive(t, dst,
			strconv.Itoa(i), DefaultCompressionType)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.addArchive(archive)
	}

	tests := []struct {
		name      string
		cli       bool
		exclude   []int
		expectErr bool
	}{
		{"local", false, nil, false},
		{"local exclude", false, []int{1}, false},
		{"local exclude multiple", false, []int{0, 2}, false},
		{"local exclude all", false, []int{0, 1, 2}, true},
		{"local cli", true, nil, false},
		{"local cli exclude", true, []int{1}, false},
		{"local cli exclude multiple", true, []int{0, 2}, false},
		{"local cli exclude all", true, []int{0, 1, 2}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repoDir := t.TempDir()
			repo, err := NewLocalRepository(t.Context(), repoDir)
			if err != nil {
				t.Fatalf("new local repository: %v", err)
			}
			url := "local:" + repoDir

			// Add snapshot to repo.
			if err = repo.SnapshotAdd(t.Context(), snapshot); err != nil {
				t.Fatalf("add snapshot %s: %v", snapshot.ID, err)
			}

			params := retrieveTestParams{
				archiveCount, url, tt.cli, tt.exclude, tt.expectErr,
			}
			testSnapshotRetrieve(t, repo, params)
		})
	}
}

func TestSnapshotRetrieveCompression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ct   CompressionType
	}{
		{"default", DefaultCompressionType},
		{"gzip", CompressionTypeGzip},
		{"zstd", CompressionTypeZstd},
		{"none", CompressionTypeNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l, err := NewLocalRepository(t.Context(), t.TempDir())
			if err != nil {
				t.Fatalf("new local repository: %v", err)
			}

			testSnapshotRetrieveCompression(t, l, tt.ct)
		})
	}
}

type retrieveTestParams struct {
	archiveCount int
	url          string
	cli          bool
	exclude      []int
	expectErr    bool
}

func testSnapshotRetrieve(t *testing.T, repo Repository, tt retrieveTestParams) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	ls, err := repo.SnapshotList(ctx)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}

	// Test retrieving and extracting a snapshot's archives.
	dest := t.TempDir()
	if tt.cli {
		// Drive the retrieval through the `dave retrieve` CLI command
		args := []string{"retrieve", "--repo", tt.url, "-s", ls[0].ID, dest}

		if len(tt.exclude) > 0 {
			ex := make([]string, 0, len(tt.exclude))
			for _, i := range tt.exclude {
				ex = append(ex, fmt.Sprintf("archive-%d.tar.gz", i))
			}
			args = append(args, "--exclude", strings.Join(ex, ","))
		}

		_, stderr, err := runDaveCLI(t, ctx, t.TempDir(), args...)
		if err != nil {
			if !tt.expectErr {
				t.Fatalf("dave retrieve: %v\nstderr: %s", err, stderr)
			}
		} else if tt.expectErr {
			t.Fatal("expected retrieve error")
		}
	} else {
		ex := make(map[string]struct{}, len(tt.exclude))
		for _, i := range tt.exclude {
			ex[fmt.Sprintf("archive-%d.tar.gz", i)] = struct{}{}
		}
		err = repo.SnapshotRetrieve(ctx, ls[0].ID, dest, ex)
		if err != nil {
			if !tt.expectErr {
				t.Fatalf("retrieve snapshot %s: %v", ls[0].ID, err)
			}
		} else if tt.expectErr {
			t.Fatal("expected retrieve error")
		}
	}
	for i := range tt.archiveCount {
		fileName := fmt.Sprintf("file-%d.txt", i)
		_, err = os.Stat(filepath.Join(dest, fmt.Sprintf("data-%d", i), fileName))
		if err != nil {
			if !slices.Contains(tt.exclude, i) {
				t.Fatalf("archive %d file %s not extracted: %v", i, fileName, err)
			}
		} else if slices.Contains(tt.exclude, i) {
			t.Fatalf("expected archive %s to not be extracted", fileName)
		}
	}
}

func testSnapshotRetrieveCompression(t *testing.T, l Repository, ct CompressionType) {
	dst := t.TempDir()
	snapshot := NewSnapshot()

	archive, err := addCustomTestArchive(t, dst,
		"test", ct)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.addArchive(archive)

	// Add snapshot to repo.
	if err = l.SnapshotAdd(t.Context(), snapshot); err != nil {
		t.Fatalf("add snapshot %s: %v", snapshot.ID, err)
	}

	// Test listing snapshots.
	ls, err := l.SnapshotList(t.Context())
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(ls) != 1 {
		t.Fatalf("want 1 snapshot, got %d", len(ls))
	}
	if ls[0].ID != snapshot.ID {
		t.Fatalf("want snapshot %s, got %s",
			snapshot.ID, ls[0].ID)
	}
	if len(ls[0].Archives) != 1 {
		t.Fatalf("expected 1 archive, got %d", len(ls[0].Archives))
	}
	if ls[0].Archives[0].Compression != ct {
		t.Fatalf("expected compression %s, got %s",
			ct, ls[0].Archives[0].Compression)
	}

	// Test retrieving and extracting a snapshot's archives.
	retrievalDir := t.TempDir()

	if err = l.SnapshotRetrieve(t.Context(), ls[0].ID, retrievalDir, nil); err != nil {
		t.Fatalf("retrieve snapshot %s: %v", ls[0].ID, err)
	}
	_, err = os.Stat(filepath.Join(retrievalDir, "data-test", "file-test.txt"))
	if err != nil {
		t.Fatal("archive not extracted")
	}
}

func addCustomTestArchive(t *testing.T, dst, name string, ct CompressionType) (*SnapshotArchive, error) {
	t.Helper()

	archiveRepo, err := NewLocalRepository(t.Context(), dst)
	if err != nil {
		return nil, fmt.Errorf("new local repository: %v", err)
	}
	d, err := NewDave(archiveRepo, testDefaultConfig(t))
	if err != nil {
		t.Fatalf("new dave: %v", err)
	}

	src := t.TempDir()

	srcDir := filepath.Join(src, fmt.Sprintf("data-%s", name))
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %v", srcDir, err)
	}
	fileName := fmt.Sprintf("file-%s.txt", name)
	contents := fmt.Sprintf("archive %s contents", name)
	err = os.WriteFile(filepath.Join(srcDir, fileName), []byte(contents), 0o700)
	if err != nil {
		return nil, fmt.Errorf("write %s: %v", fileName, err)
	}

	archive, err := d.archive(t.Context(), fmt.Sprintf("archive-%s", name),
		dst, srcDir, ct)
	if err != nil {
		return nil, fmt.Errorf("archive %s: %v", name, err)
	}

	return archive, nil
}

// testSnapshotWithArchives creates a test snapshot with a defined number of archives.
func testSnapshotWithArchives(archives int) *Snapshot {
	snapshot := NewSnapshot()
	for i := range archives {
		snapshot.addArchive(&SnapshotArchive{
			Name:        fmt.Sprintf("test-%d.tar.gz", i),
			Checksums:   testDBHashes,
			Size:        1,
			Compression: CompressionTypeGzip,
			path:        testDBArchive,
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
