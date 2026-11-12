// Copyright (c) 2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestPathExists(t *testing.T) {
	_, err := NewS3Repository(t.Context(), "http://blah.test")
	if !errors.Is(err, ErrNoS3URLPath) {
		t.Fatalf("unexpected error: %s, expected %s", err, ErrNoS3URLPath)
	}
}

// mockS3Object is a single object stored in a mockS3Server.
type mockS3Object struct {
	data        []byte
	contentType string
}

// mockS3Server is a minimal in-process fake of the S3/MinIO REST API,
// implementing just enough of ListObjectsV2 and GetObject for
// NewS3Repository and SnapshotList to work against it.
type mockS3Server struct {
	*httptest.Server
	bucket string

	mu      sync.RWMutex
	objects map[string]mockS3Object // key: object key (bucket-relative, no leading slash)

	// cut off responses after dropAfter bytes, up to maxDrops
	dropAfter int
	maxDrops  int

	// failGet is returned as the status of every GET, if set
	failGet int
	gets    int

	// multipart uploads, by upload ID
	uploads map[string]*mockS3Upload
	lastID  int
	// dropPart reports whether to drop the connection of a part upload
	dropPart func(key string) bool
}

type mockS3Upload struct {
	key         string
	contentType string
	parts       map[int][]byte
}

// newMockS3Server starts a mock S3 server for the given bucket.
func newMockS3Server(t *testing.T, bucket string) *mockS3Server {
	t.Helper()
	m := &mockS3Server{
		bucket:  bucket,
		objects: make(map[string]mockS3Object),
		uploads: make(map[string]*mockS3Upload),
	}
	m.Server = httptest.NewServer(http.HandlerFunc(m.handleRequest))
	t.Cleanup(m.Close)
	return m
}

// putObject seeds the store directly, bypassing any S3 PutObject call -
// this fake doesn't need to support uploads for NewS3Repository/SnapshotList.
func (m *mockS3Server) putObject(key, contentType string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = mockS3Object{data: data, contentType: contentType}
}

// url returns a repository connection string for NewS3Repository, e.g.
// "http://accessKey:secret@127.0.0.1:PORT/bucket[/prefix]".
func (m *mockS3Server) url(prefix string) string {
	u, err := url.Parse(m.Server.URL)
	if err != nil {
		panic(err)
	}
	p := m.bucket
	if prefix != "" {
		p += "/" + prefix
	}
	return fmt.Sprintf("http://mockKey:mockSecret@%s/%s", u.Host, p)
}

// mockListBucketResult and mockListContent mirror minio.ListBucketV2Result
// and minio.ObjectInfo's XML shape closely enough for xml.Marshal to
// produce something minio-go's decoder round-trips correctly (no xml tags
// needed - field names already match).
type mockListBucketResult struct {
	XMLName     xml.Name `xml:"ListBucketResult"`
	Name        string
	Prefix      string
	MaxKeys     int64
	IsTruncated bool
	Contents    []mockListContent
}

type mockListContent struct {
	Key          string
	LastModified string
	ETag         string
	Size         int64
	StorageClass string
}

// handleRequest dispatches to the fakes based on the request method and the
// query parameters minio-go sets for each request type.
func (m *mockS3Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case q.Has("uploads"):
		m.handleInitiateUpload(w, r)
	case q.Has("uploadId") && r.Method == http.MethodPut:
		m.handlePutPart(w, r)
	case q.Has("uploadId") && r.Method == http.MethodPost:
		m.handleCompleteUpload(w, r)
	case q.Has("uploadId") && r.Method == http.MethodDelete:
		m.mu.Lock()
		delete(m.uploads, q.Get("uploadId"))
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete:
		m.mu.Lock()
		delete(m.objects, m.objectKey(r))
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case len(q) != 0:
		m.handleList(w, r)
	default:
		m.handleGetObject(w, r)
	}
}

func (m *mockS3Server) objectKey(r *http.Request) string {
	return strings.TrimPrefix(r.URL.Path, "/"+m.bucket+"/")
}

func (m *mockS3Server) object(key string) (mockS3Object, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	obj, ok := m.objects[key]
	return obj, ok
}

func (m *mockS3Server) objectCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.objects)
}

func (m *mockS3Server) uploadCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.uploads)
}

func (m *mockS3Server) handleInitiateUpload(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.lastID++
	id := strconv.Itoa(m.lastID)
	m.uploads[id] = &mockS3Upload{
		key:         m.objectKey(r),
		contentType: r.Header.Get("Content-Type"),
		parts:       make(map[int][]byte),
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
}

func (m *mockS3Server) handlePutPart(w http.ResponseWriter, r *http.Request) {
	if m.dropPart != nil && m.dropPart(m.objectKey(r)) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			panic(err)
		}
		_ = conn.Close()
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))

	m.mu.Lock()
	m.uploads[r.URL.Query().Get("uploadId")].parts[n] = data
	m.mu.Unlock()
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, n))
}

func (m *mockS3Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[r.URL.Query().Get("uploadId")]
	w.Header().Set("Content-Type", "application/xml")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, "<Error><Code>NoSuchUpload</Code></Error>")
		return
	}
	var data []byte
	for i := 1; i <= len(u.parts); i++ {
		data = append(data, u.parts[i]...)
	}
	m.objects[u.key] = mockS3Object{data: data, contentType: u.contentType}
	delete(m.uploads, r.URL.Query().Get("uploadId"))

	_, _ = fmt.Fprintf(w, "<CompleteMultipartUploadResult><Bucket>%s</Bucket></CompleteMultipartUploadResult>", m.bucket)
}

func (m *mockS3Server) handleGetObject(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+m.bucket+"/")

	m.mu.Lock()
	obj, ok := m.objects[key]
	drop := ok && r.Method == http.MethodGet && m.maxDrops > 0
	if drop {
		m.maxDrops--
	}
	failGet := 0
	if r.Method == http.MethodGet {
		m.gets++
		failGet = m.failGet
	}
	m.mu.Unlock()
	if failGet != 0 {
		w.WriteHeader(failGet)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if drop {
		w = &limitWriter{ResponseWriter: w, n: m.dropAfter}
	}

	w.Header().Set("Content-Type", obj.contentType)
	w.Header().Set("ETag", `"mock-etag"`)
	http.ServeContent(w, r, "", time.Now(), bytes.NewReader(obj.data))
}

type limitWriter struct {
	http.ResponseWriter
	n int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if len(p) > w.n {
		n, _ := w.ResponseWriter.Write(p[:w.n])
		w.n = 0
		return n, io.ErrShortWrite
	}
	w.n -= len(p)
	return w.ResponseWriter.Write(p)
}

func (m *mockS3Server) handleList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")

	m.mu.RLock()
	result := mockListBucketResult{Name: m.bucket, Prefix: prefix, MaxKeys: 1000}
	for key, obj := range m.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}

		result.Contents = append(result.Contents, mockListContent{
			Key:          key,
			LastModified: "2006-01-02T15:04:05Z",
			ETag:         "mock-etag",
			Size:         int64(len(obj.data)),
			StorageClass: "STANDARD",
		})
	}
	m.mu.RUnlock()

	w.Header().Set("Content-Type", "application/xml")
	if err := xml.NewEncoder(w).Encode(result); err != nil {
		panic(err)
	}
}

func TestSnapshotListIngoresTopLevelMetadataJSONFile(t *testing.T) {
	const (
		bucket = "test-bucket"
		prefix = "backups"
	)
	srv := newMockS3Server(t, bucket)

	// Top-level repository meta.json - must NOT show up in SnapshotList.
	srv.putObject(prefix+"/"+metaFilename, metaContentType, []byte(`{"latest":null}`))

	// Two snapshot meta.json files, newest last, plus an archive file that
	// must also be excluded from the snapshot list.
	snap1 := testSnapshotWithArchives(0)
	snap2 := testSnapshotWithArchives(0)
	snap2.Time = snap1.Time.Add(time.Hour)

	for _, snap := range []*Snapshot{snap1, snap2} {
		b, err := json.Marshal(snap)
		if err != nil {
			t.Fatalf("marshal snapshot %s: %v", snap.ID, err)
		}
		srv.putObject(prefix+"/"+snap.ID+"/"+metaFilename, metaContentType, b)
	}
	srv.putObject(prefix+"/"+snap1.ID+"/archive.tar.gz", "application/gzip", []byte("data"))

	s3, err := NewS3Repository(t.Context(), srv.url(prefix))
	if err != nil {
		t.Fatalf("new S3 backend: %v", err)
	}

	got, err := s3.SnapshotList(t.Context())
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 snapshots, got %d (check for meta.json?)", len(got))
	}

	// Snapshots are sorted newest to oldest.
	want := []*Snapshot{snap2, snap1}
	for i, snap := range got {
		if snap.ID != want[i].ID {
			t.Errorf("snapshot %d: want ID %s, got %s", i, want[i].ID, snap.ID)
		}
	}
}

func TestDownloadFileResume(t *testing.T) {
	data := make([]byte, 100000)
	rand.Read(data)

	tests := []struct {
		name      string
		part      []byte
		dropAfter int
		drops     int
		wantErr   error
	}{
		{name: "full download"},
		{name: "resume after drop", dropAfter: 25000, drops: 1},
		{name: "progress resets attempts", dropAfter: 5000, drops: 15},
		{name: "resume existing part", part: data[:1000]},
		{name: "oversized part", part: append(slices.Clone(data), 0), wantErr: ErrInvalidPartFile},
		{name: "no progress", drops: 100, wantErr: io.ErrUnexpectedEOF},
	}
	for _, tti := range tests {
		t.Run(tti.name, func(t *testing.T) {
			s := newMockS3Server(t, "bucket")
			s.putObject("snap/archive", "application/test", data)
			s.dropAfter = tti.dropAfter
			s.maxDrops = tti.drops

			repo, err := NewS3Repository(t.Context(), s.url(""))
			if err != nil {
				t.Fatal(err)
			}
			s3 := repo.(*s3Repository)
			s3.retryMinDelay = time.Millisecond

			filename := filepath.Join(t.TempDir(), "archive")
			if tti.part != nil {
				if err := os.WriteFile(filename+".snap.part", tti.part, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			err = s3.downloadFile(t.Context(), "snap", "archive", filename)
			if !errors.Is(err, tti.wantErr) {
				t.Fatalf("expected error %v, got %v", tti.wantErr, err)
			}
			if err != nil {
				return
			}

			got, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("downloaded data mismatch")
			}
		})
	}
}

func TestDownloadFilePermanentError(t *testing.T) {
	s := newMockS3Server(t, "bucket")
	s.putObject("snap/archive", "application/test", []byte("data"))
	s.failGet = http.StatusForbidden
	s3 := newMockS3Repository(t, s)

	err := s3.downloadFile(t.Context(), "snap", "archive", filepath.Join(t.TempDir(), "archive"))
	var resp minio.ErrorResponse
	if !errors.As(err, &resp) || resp.Code != minio.AccessDenied {
		t.Fatalf("expected %s, got %v", minio.AccessDenied, err)
	}
	if s.gets != 1 {
		t.Fatalf("expected 1 GET, got %d", s.gets)
	}
}

// newMockS3Repository returns an S3 repository backed by the mock server, with
// fast upload retries. The minio client does not retry, so dropped requests
// reach dave's retries.
func newMockS3Repository(t *testing.T, m *mockS3Server) *s3Repository {
	t.Helper()
	repo, err := NewS3Repository(t.Context(), m.url(""))
	if err != nil {
		t.Fatal(err)
	}
	s3 := repo.(*s3Repository)
	s3.minioClient, err = minio.New(strings.TrimPrefix(m.URL, "http://"), &minio.Options{
		Creds:      credentials.NewStaticV4("mockKey", "mockSecret", ""),
		Region:     "us-east-1",
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	s3.partSize = 1000
	s3.retryMinDelay = time.Millisecond
	return s3
}

// testUploadArchive writes size random bytes to a file and returns it as an
// archive.
func testUploadArchive(t *testing.T, name string, size int) (*SnapshotArchive, []byte) {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return &SnapshotArchive{Name: name, Size: uint64(size), Compression: CompressionTypeGzip, path: path}, data
}

func TestUploadArchive(t *testing.T) {
	tests := []struct {
		name    string
		drops   int
		wantErr bool
	}{
		{name: "multiple parts"},
		{name: "retry dropped parts", drops: 5},
		{name: "give up after max attempts", drops: 1 << 20, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newMockS3Server(t, "bucket")
			var mx sync.Mutex
			drops := tt.drops
			srv.dropPart = func(string) bool {
				mx.Lock()
				defer mx.Unlock()
				drops--
				return drops >= 0
			}
			s3 := newMockS3Repository(t, srv)
			archive, data := testUploadArchive(t, "archive.tar.gz", 2500)

			err := s3.uploadArchive(t.Context(), "snap", archive)
			if tt.wantErr != (err != nil) {
				t.Fatalf("upload error = %v, want error %v", err, tt.wantErr)
			}
			if n := srv.uploadCount(); n != 0 {
				t.Errorf("%d multipart uploads left behind", n)
			}
			obj, ok := srv.object("snap/archive.tar.gz")
			if tt.wantErr {
				if ok {
					t.Error("object should not exist after failed upload")
				}
				return
			}
			if !bytes.Equal(obj.data, data) {
				t.Fatal("uploaded data mismatch")
			}
		})
	}
}

func TestS3SnapshotCleanup(t *testing.T) {
	srv := newMockS3Server(t, "bucket")
	srv.dropPart = func(key string) bool { return strings.HasSuffix(key, "/b.tar.gz") }
	s3 := newMockS3Repository(t, srv)

	snapshot := NewSnapshot()
	a, _ := testUploadArchive(t, "a.tar.gz", 500)
	b, _ := testUploadArchive(t, "b.tar.gz", 2500)
	snapshot.addArchive(a)
	snapshot.addArchive(b)

	if err := s3.SnapshotAdd(t.Context(), snapshot); err == nil {
		t.Fatal("expected snapshot add error")
	}
	if n := srv.objectCount(); n != 0 {
		t.Errorf("%d objects left behind", n)
	}
	if n := srv.uploadCount(); n != 0 {
		t.Errorf("%d multipart uploads left behind", n)
	}
}
