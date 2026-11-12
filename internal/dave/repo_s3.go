// Copyright (c) 2025-2026 Hemi Labs, Inc.
// Use of this source code is governed by the MIT License,
// which can be found in the LICENSE file.

package dave

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // S3 Content-MD5 and ETags are MD5.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/sync/errgroup"
)

const (
	maxRetryAttempts   = 10
	retryMinDelay      = 1 * time.Second
	retryMaxDelay      = 10 * time.Second
	uploadPartSize     = 64 << 20
	maxUploadParts     = 10000
	uploadConcurrency  = 4
	uploadRetryTimeout = 2 * time.Minute
	cleanupTimeout     = 2 * time.Minute
)

// permanentS3Codes are S3 error codes which are not worth retrying.
var permanentS3Codes = []string{
	minio.AccessDenied,
	minio.InvalidAccessKeyID,
	minio.SignatureDoesNotMatch,
	minio.NoSuchBucket,
	minio.NoSuchUpload,
	minio.NoSuchKey,
}

var (
	ErrNoS3URLPath     = errors.New("s3 URL missing bucket path")
	ErrInvalidPartFile = errors.New("invalid partial file")
)

// s3Config is the configuration for interacting with S3-compatible storage.
type s3Config struct {
	Endpoint     string
	UseHTTP      bool
	KeyID        string
	Secret       string
	Bucket       string
	Prefix       string
	Region       string
	StorageClass string
}

func defaultS3Config() s3Config {
	return s3Config{}
}

func parseS3URL(s string) (s3Config, error) {
	u, err := url.Parse(s)
	if err != nil {
		return s3Config{}, err
	}
	if !slices.Contains([]string{"s3", "http", "https"}, u.Scheme) {
		return s3Config{}, fmt.Errorf("invalid scheme: %s", u.Scheme)
	}
	if len(u.Path) == 0 {
		return s3Config{}, fmt.Errorf("%s: %w", s, ErrNoS3URLPath)
	}
	bucket, prefix, _ := strings.Cut(u.Path[1:], "/")

	c := defaultS3Config()
	c.Endpoint = u.Host
	c.UseHTTP = u.Scheme == "http"
	if u.User != nil {
		c.KeyID = u.User.Username()
		c.Secret, _ = u.User.Password()
	}
	c.Bucket = bucket
	if prefix != "" {
		c.Prefix = path.Clean(prefix)
	}

	c.readEnv()
	return c, nil
}

func (c *s3Config) readEnv() {
	if c.KeyID == "" {
		c.KeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if c.Secret == "" {
		c.Secret = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	if c.Region == "" {
		c.Region = os.Getenv("AWS_DEFAULT_REGION")
	}
}

// s3Repository is a repository which stores snapshots in S3-compatible storage.
type s3Repository struct {
	conf s3Config

	minioClient *minio.Client

	// testing overrides
	retryMinDelay time.Duration
	partSize      int64
}

var _ Repository = (*s3Repository)(nil)

// NewS3Repository creates a new S3 storage backend.
func NewS3Repository(_ context.Context, s string) (Repository, error) {
	conf, err := parseS3URL(s)
	if err != nil {
		return nil, fmt.Errorf("parse s3 url: %w", err)
	}

	// same as minio.DefaultTransport with increased timeouts.
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   16,
		ResponseHeaderTimeout: 10 * time.Minute,
		IdleConnTimeout:       5 * time.Minute,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 10 * time.Second,
		DisableCompression:    true, // disable gzip compression for the HTTP requests
	}

	minioClient, err := minio.New(conf.Endpoint, &minio.Options{
		Creds:           credentials.NewStaticV4(conf.KeyID, conf.Secret, ""),
		Secure:          !conf.UseHTTP,
		Region:          conf.Region,
		TrailingHeaders: true,
		Transport:       transport,
	})
	if err != nil {
		return nil, fmt.Errorf("new minio client: %w", err)
	}

	return &s3Repository{
		conf:          conf,
		minioClient:   minioClient,
		retryMinDelay: retryMinDelay,
		partSize:      uploadPartSize,
	}, nil
}

func (s *s3Repository) Metadata(ctx context.Context) (*RepositoryMeta, error) {
	obj, err := s.getFile(ctx, metaFilename)
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	var meta RepositoryMeta
	if err = json.NewDecoder(obj).Decode(&meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (s *s3Repository) MetadataUpdate(ctx context.Context, meta *RepositoryMeta) error {
	b, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err = s.uploadFile(ctx, metaFilename, metaContentType, b); err != nil {
		return fmt.Errorf("upload meta: %w", err)
	}
	return nil
}

// SnapshotAdd uploads a snapshot and its archives to S3.
func (s *s3Repository) SnapshotAdd(ctx context.Context, snapshot *Snapshot) (err error) {
	defer func() {
		if err != nil {
			s.removeFailedSnapshot(snapshot)
		}
	}()

	// Upload snapshot archives.
	errg, ectx := errgroup.WithContext(ctx)
	for _, archive := range snapshot.Archives {
		errg.Go(func() error {
			return s.uploadArchive(ectx, snapshot.ID, archive)
		})
	}
	if err := errg.Wait(); err != nil {
		return fmt.Errorf("add snapshot archive: %w", err)
	}

	// Upload snapshot metadata file.
	b, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal snapshot meta: %w", err)
	}
	if err = s.uploadFile(ctx, filepath.Join(snapshot.ID, metaFilename), metaContentType, b); err != nil {
		return fmt.Errorf("upload snapshot meta: %w", err)
	}

	return nil
}

// removeFailedSnapshot removes the objects of a snapshot whose upload failed,
// so it does not leave unlisted objects behind.
func (s *s3Repository) removeFailedSnapshot(snapshot *Snapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	remove := func(name string) {
		key := filepath.Join(s.conf.Prefix, snapshot.ID, name)
		err := s.removeObject(ctx, key)
		if err != nil && minio.ToErrorResponse(err).Code != minio.NoSuchKey {
			slog.Error("Failed to remove failed snapshot object", "key", key, "err", err)
		}
	}
	remove(metaFilename)
	for _, archive := range snapshot.Archives {
		remove(archive.Name)
	}
}

// SnapshotByID retrieves snapshot metadata by a snapshot ID.
func (s *s3Repository) SnapshotByID(ctx context.Context, id string) (*Snapshot, error) {
	obj, err := s.getFile(ctx, filepath.Join(id, metaFilename))
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrSnapshotNotExist
		}
		return nil, err
	}
	defer obj.Close()

	var snapshot Snapshot
	if err = json.NewDecoder(obj).Decode(&snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// SnapshotList lists the snapshots stored in S3.
func (s *s3Repository) SnapshotList(ctx context.Context) ([]*Snapshot, error) {
	opts := minio.ListObjectsOptions{
		Prefix:    s.conf.Prefix,
		Recursive: true,
	}
	var snapshotMetaFiles []string
	for obj := range s.minioClient.ListObjects(ctx, s.conf.Bucket, opts) {
		if obj.Err != nil {
			return nil, fmt.Errorf("list objects: %w", obj.Err)
		}

		// Look for snapshot metadata keys, e.g. <snapshotID>/meta.json.
		// Exclude the repository-level meta file, which sits directly
		// under the prefix (<prefix>/meta.json) and also ends in
		// "/meta.json".
		key := strings.TrimPrefix(obj.Key, s.conf.Prefix+"/")
		if key != metaFilename && strings.HasSuffix(obj.Key, "/"+metaFilename) {
			snapshotMetaFiles = append(snapshotMetaFiles, key)
		}
	}

	var (
		mx        sync.Mutex
		snapshots []*Snapshot
	)
	errg, ectx := errgroup.WithContext(ctx)
	for _, metaFile := range snapshotMetaFiles {
		errg.Go(func() error {
			// Retrieve snapshot metadata file.
			obj, err := s.getFile(ectx, metaFile)
			if err != nil {
				return err
			}
			defer obj.Close()

			// Decode snapshot metadata json.
			var snapshot Snapshot
			if err = json.NewDecoder(obj).Decode(&snapshot); err != nil {
				return err
			}
			mx.Lock()
			snapshots = append(snapshots, &snapshot)
			mx.Unlock()
			return nil
		})
	}
	if err := errg.Wait(); err != nil {
		return nil, err
	}

	// Sort newest to oldest.
	slices.SortStableFunc(snapshots, func(a, b *Snapshot) int {
		return b.Time.Compare(a.Time)
	})
	return snapshots, nil
}

// SnapshotRemove removes a snapshot and all archives.
func (s *s3Repository) SnapshotRemove(ctx context.Context, id string) error {
	snapshot, err := s.SnapshotByID(ctx, id)
	if err != nil {
		return err
	}

	for _, archive := range snapshot.Archives {
		// Remove objects one at a time rather than using RemoveObjects. A single
		// delete has no response body, so a connection dropped while the server
		// is still deleting is retried by minio, whereas a multi-object delete
		// fails with "unexpected EOF" while reading its response.
		key := filepath.Join(s.conf.Prefix, snapshot.ID, archive.Name)
		if rerr := s.removeObject(ctx, key); rerr != nil {
			err = errors.Join(err, fmt.Errorf("remove %s %s: %w", snapshot.ID, key, rerr))
		}
	}
	if err != nil {
		return err
	}
	// Archives are removed first and the meta file last, so a failure leaves
	// the snapshot listed and the removal can be retried.
	metaKey := filepath.Join(s.conf.Prefix, snapshot.ID, metaFilename)
	if err = s.removeObject(ctx, metaKey); err != nil {
		return fmt.Errorf("remove %s %s: %w", snapshot.ID, metaKey, err)
	}
	return nil
}

// removeObject removes an object from S3. If the removal fails for a reason
// other than the object not existing, the object is checked for existence and
// the error is ignored if it no longer exists, since the connection may drop
// after the server has already completed the removal.
func (s *s3Repository) removeObject(ctx context.Context, key string) error {
	err := s.minioClient.RemoveObject(ctx, s.conf.Bucket, key, minio.RemoveObjectOptions{})
	if err != nil && minio.ToErrorResponse(err).Code != minio.NoSuchKey {
		_, serr := s.minioClient.StatObject(ctx, s.conf.Bucket, key, minio.StatObjectOptions{})
		if minio.ToErrorResponse(serr).Code == minio.NoSuchKey {
			slog.Warn("Object removed despite error", "key", key, "err", err)
			return nil
		}
	}
	return err
}

// SnapshotRetrieve downloads a snapshot's archives to dest and extracts them.
func (s *s3Repository) SnapshotRetrieve(ctx context.Context, id, dest string, exclude map[string]ExcludeMode) error {
	snapshot, err := s.SnapshotByID(ctx, id)
	if err != nil {
		return err
	}

	dir, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Download every archive to dest, then extract it.
	var count int
	errg, ectx := errgroup.WithContext(ctx)
	for _, ar := range snapshot.Archives {
		var exc ExcludeMode
		if mode, ok := exclude[ar.Name]; ok {
			if mode == SkipAll {
				slog.Info("Skipping archive", "archive", ar.Name)
				continue
			}
			exc = mode
		}
		errg.Go(func() error {
			archivePath := filepath.Join(dir, ar.Name)
			if exc != SkipDownload {
				slog.Info("Downloading archive", "archive", ar.Name)
				err := s.downloadFile(ectx, snapshot.ID, ar.Name, archivePath)
				if err != nil {
					return fmt.Errorf("download archive %s: %w", ar.Name, err)
				}
				slog.Info("Archive successfully downloaded", "archive", ar.Name)
			} else {
				slog.Info("Skipping download", "archive", ar.Name)
			}
			if exc != SkipExtraction {
				slog.Info("Extracting archive", "archive", ar.Name)
				err := extractArchive(ectx, archivePath, dir, ar.Compression)
				if err != nil {
					return fmt.Errorf("extract archive %s: %w", ar.Name, err)
				}
				slog.Info("Archive successfully extracted", "archive", ar.Name)
			} else {
				slog.Info("Skipping extraction", "archive", ar.Name)
			}

			return nil
		})
		count++
	}
	if count < 1 {
		return errors.New("no archives retrieved")
	}
	if err = errg.Wait(); err != nil {
		return fmt.Errorf("retrieve snapshot archives: %w", err)
	}

	return nil
}

func (s *s3Repository) getFile(ctx context.Context, objectName string) (*minio.Object, error) {
	objectPath := filepath.Join(s.conf.Prefix, objectName)
	obj, err := s.minioClient.GetObject(ctx, s.conf.Bucket, objectPath, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", objectPath, err)
	}
	return obj, nil
}

// downloadFile downloads a snapshot file from S3 to the given local path.
//
// The object is written to a .part and renamed once completed.
// If the connection drops mid-download, the download is resumed
// from the end of the part file. The part file name includes the snapshot ID,
// so a part file left behind by a previous retrieve of the same snapshot can
// also be resumed
func (s *s3Repository) downloadFile(ctx context.Context, snapshotID, name, filename string) error {
	objectPath := filepath.Join(s.conf.Prefix, snapshotID, name)
	info, err := s.minioClient.StatObject(ctx, s.conf.Bucket, objectPath, minio.StatObjectOptions{})
	if err != nil {
		return fmt.Errorf("stat %s: %w", objectPath, err)
	}

	partPath := filename + "." + snapshotID + ".part"
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	err = s.retry(ctx, "download "+objectPath, 0, func(ctx context.Context) (bool, error) {
		var n int64
		var err error
		if offset < info.Size {
			n, err = s.downloadRange(ctx, objectPath, offset, f)
			offset += n
		}
		if offset > info.Size {
			return false, ErrInvalidPartFile
		}
		if err == nil && offset < info.Size {
			err = io.ErrUnexpectedEOF
		}
		return n > 0, err
	})
	if errors.Is(err, ErrInvalidPartFile) {
		_ = f.Close()
		if rerr := os.Remove(partPath); rerr != nil {
			return fmt.Errorf("remove %s: %w", partPath, rerr)
		}
		return fmt.Errorf("%s: %w", partPath, err)
	}
	if err != nil {
		return err
	}

	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(partPath, filename)
}

func (s *s3Repository) downloadRange(ctx context.Context, objectPath string, offset int64, w io.Writer) (int64, error) {
	var opts minio.GetObjectOptions
	if offset > 0 {
		if err := opts.SetRange(offset, 0); err != nil {
			return 0, err
		}
	}

	obj, err := s.minioClient.GetObject(ctx, s.conf.Bucket, objectPath, opts)
	if err != nil {
		return 0, err
	}
	defer obj.Close()

	return io.Copy(w, obj)
}

type retryHook func(ctx context.Context) (progressed bool, err error)

// retry calls rh until it succeeds, ctx is done, it returns a permanent S3
// error, or maxRetryAttempts attempts have been made without progress.
func (s *s3Repository) retry(ctx context.Context, op string, timeout time.Duration, rh retryHook) error {
	delay := s.retryMinDelay
	var attempt int
	for {
		attemptCtx, cancel := ctx, func() {}
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		progressed, err := rh(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrInvalidPartFile) ||
			slices.Contains(permanentS3Codes, minio.ToErrorResponse(err).Code) {
			return err
		}

		if progressed {
			attempt = 0
			delay = s.retryMinDelay
		}
		attempt++
		if attempt >= maxRetryAttempts {
			return fmt.Errorf("%s: giving up after %d attempts: %w", op, attempt, err)
		}

		slog.Warn("Request failed, retrying",
			"op", op, "attempt", attempt, "delay", delay, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, retryMaxDelay)
	}
}

// uploadFile uploads a file to S3.
func (s *s3Repository) uploadFile(ctx context.Context, filename, contentType string, b []byte) error {
	objectPath := filepath.Join(s.conf.Prefix, filename)
	err := s.retry(ctx, "upload "+objectPath, uploadRetryTimeout, func(ctx context.Context) (bool, error) {
		_, err := s.minioClient.PutObject(ctx, s.conf.Bucket, objectPath,
			bytes.NewReader(b), int64(len(b)), minio.PutObjectOptions{
				ContentType:  contentType,
				StorageClass: s.conf.StorageClass,
			})
		return false, err
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", objectPath, err)
	}
	return nil
}

// uploadArchive uploads a snapshot archive to S3 as a multipart upload,
// retrying each part separately. The upload is aborted if it fails.
func (s *s3Repository) uploadArchive(ctx context.Context, snapshotID string, archive *SnapshotArchive) (err error) {
	objectPath := filepath.Join(s.conf.Prefix, snapshotID, archive.Name)
	f, err := os.Open(archive.path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()

	core := minio.Core{Client: s.minioClient}
	opts := minio.PutObjectOptions{
		ContentType:  archive.Compression.contentType(),
		StorageClass: s.conf.StorageClass,
	}
	var uploadID string
	err = s.retry(ctx, "create upload "+objectPath, uploadRetryTimeout, func(ctx context.Context) (_ bool, err error) {
		uploadID, err = core.NewMultipartUpload(ctx, s.conf.Bucket, objectPath, opts)
		return false, err
	})
	if err != nil {
		return fmt.Errorf("create upload %s: %w", objectPath, err)
	}
	defer func() {
		if err != nil {
			s.abortUpload(core, objectPath, uploadID)
			err = fmt.Errorf("upload snapshot archive (%s): %w", objectPath, err)
		}
	}()

	// (a + b - 1) / b rounds up integers
	partSize := max(s.partSize, (size+maxUploadParts-1)/maxUploadParts)
	numParts := max(1, int((size+partSize-1)/partSize))
	slog.Info("Uploading archive", "object", objectPath, "size", size, "parts", numParts)

	parts := make([]minio.CompletePart, numParts)
	partMD5s := make([][]byte, numParts)
	errg, ectx := errgroup.WithContext(ctx)
	errg.SetLimit(uploadConcurrency)
	for i := range numParts {
		errg.Go(func() (err error) {
			off := int64(i) * partSize
			r := io.NewSectionReader(f, off, min(partSize, size-off))
			parts[i].PartNumber = i + 1
			parts[i].ETag, partMD5s[i], err = s.uploadPart(ectx, core, objectPath, uploadID, i+1, r)
			return err
		})
	}
	if err = errg.Wait(); err != nil {
		return err
	}

	var retried bool
	return s.retry(ctx, "complete upload "+objectPath, uploadRetryTimeout, func(ctx context.Context) (bool, error) {
		_, err := core.CompleteMultipartUpload(ctx, s.conf.Bucket, objectPath, uploadID, parts, opts)
		if retried && minio.ToErrorResponse(err).Code == minio.NoSuchUpload {
			// A previous attempt may have completed but lost its response.
			info, serr := s.minioClient.StatObject(ctx, s.conf.Bucket, objectPath, minio.StatObjectOptions{})
			if serr == nil && strings.Trim(info.ETag, `"`) == multipartETag(partMD5s) {
				return false, nil
			}
		}
		retried = true
		return false, err
	})
}

// uploadPart uploads a part of a multipart upload, returning its ETag and MD5.
func (s *s3Repository) uploadPart(ctx context.Context, core minio.Core, objectPath, uploadID string, partNumber int, r *io.SectionReader) (string, []byte, error) {
	h := md5.New() //nolint:gosec // S3 Content-MD5 and ETags are MD5.
	if _, err := io.Copy(h, r); err != nil {
		return "", nil, fmt.Errorf("hash part %d: %w", partNumber, err)
	}
	sum := h.Sum(nil)

	var etag string
	op := fmt.Sprintf("upload part %d of %s", partNumber, objectPath)
	err := s.retry(ctx, op, uploadRetryTimeout, func(ctx context.Context) (bool, error) {
		part, err := core.PutObjectPart(ctx, s.conf.Bucket, objectPath, uploadID, partNumber,
			io.NewSectionReader(r, 0, r.Size()), r.Size(), minio.PutObjectPartOptions{
				Md5Base64:            base64.StdEncoding.EncodeToString(sum),
				DisableContentSha256: true,
			})
		etag = part.ETag
		return false, err
	})
	return etag, sum, err
}

// multipartETag returns the ETag S3 gives an object completed from parts with
// the given MD5 sums.
func multipartETag(partMD5s [][]byte) string {
	sum := md5.Sum(bytes.Join(partMD5s, nil)) //nolint:gosec // S3 multipart ETags are MD5.
	return hex.EncodeToString(sum[:]) + "-" + strconv.Itoa(len(partMD5s))
}

// abortUpload aborts a multipart upload, so its parts are not left behind.
func (s *s3Repository) abortUpload(core minio.Core, objectPath, uploadID string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	err := s.retry(ctx, "abort upload "+objectPath, uploadRetryTimeout, func(ctx context.Context) (bool, error) {
		return false, core.AbortMultipartUpload(ctx, s.conf.Bucket, objectPath, uploadID)
	})
	if err != nil && minio.ToErrorResponse(err).Code != minio.NoSuchUpload {
		slog.Error("Failed to abort upload", "object", objectPath, "upload_id", uploadID, "err", err)
	}
}
