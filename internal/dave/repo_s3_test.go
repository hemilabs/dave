// Copyright (c) 2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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
}

// newMockS3Server starts a mock S3 server for the given bucket.
func newMockS3Server(t *testing.T, bucket string) *mockS3Server {
	t.Helper()
	m := &mockS3Server{bucket: bucket, objects: make(map[string]mockS3Object)}
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

// handleRequest dispatches to the ListObjectsV2 or GetObject fakes based on
// the "list-type" query parameter minio-go sets on list requests.
func (m *mockS3Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		m.handleList(w, r)
		return
	}
	m.handleGetObject(w, r)
}

func (m *mockS3Server) handleGetObject(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+m.bucket+"/")

	m.mu.RLock()
	obj, ok := m.objects[key]
	m.mu.RUnlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", obj.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(obj.data)))
	w.Header().Set("ETag", `"mock-etag"`)
	w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	w.Write(obj.data)
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
