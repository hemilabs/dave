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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/sync/errgroup"
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
}

var _ Repository = (*s3Repository)(nil)

// NewS3Repository creates a new S3 storage backend.
func NewS3Repository(_ context.Context, s string) (Repository, error) {
	conf, err := parseS3URL(s)
	if err != nil {
		return nil, fmt.Errorf("parse s3 url: %w", err)
	}

	minioClient, err := minio.New(conf.Endpoint, &minio.Options{
		Creds:           credentials.NewStaticV4(conf.KeyID, conf.Secret, ""),
		Secure:          !conf.UseHTTP,
		Region:          conf.Region,
		TrailingHeaders: true,
	})
	if err != nil {
		return nil, fmt.Errorf("new minio client: %w", err)
	}

	return &s3Repository{conf: conf, minioClient: minioClient}, nil
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
func (s *s3Repository) SnapshotAdd(ctx context.Context, snapshot *Snapshot) error {
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

		// Look for snapshot metadata keys, e.g. <snapshotID>/meta.json
		key := strings.TrimPrefix(obj.Key, s.conf.Prefix+"/")
		if strings.HasSuffix(obj.Key, "/"+metaFilename) {
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

	objs := make(chan minio.ObjectInfo)
	go func() {
		// RepositoryMeta file.
		objs <- minio.ObjectInfo{
			Key: filepath.Join(s.conf.Prefix, snapshot.ID, metaFilename),
		}
		// Archive files.
		for _, archive := range snapshot.Archives {
			objs <- minio.ObjectInfo{
				Key: filepath.Join(s.conf.Prefix, snapshot.ID, archive.Name),
			}
		}
		close(objs)
	}()

	for r := range s.minioClient.RemoveObjects(ctx, s.conf.Bucket, objs, minio.RemoveObjectsOptions{}) {
		if r.Err != nil {
			err = errors.Join(err, fmt.Errorf("remove %s %s: %w",
				snapshot.ID, r.ObjectName, r.Err))
		}
	}
	return err
}

func (s *s3Repository) getFile(ctx context.Context, objectName string) (*minio.Object, error) {
	objectPath := filepath.Join(s.conf.Prefix, objectName)
	obj, err := s.minioClient.GetObject(ctx, s.conf.Bucket, objectPath, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", objectPath, err)
	}
	return obj, nil
}

// uploadFile uploads a file to S3.
func (s *s3Repository) uploadFile(ctx context.Context, filename, contentType string, b []byte) error {
	r := bytes.NewReader(b)
	objectPath := filepath.Join(s.conf.Prefix, filename)
	_, err := s.minioClient.PutObject(ctx, s.conf.Bucket, objectPath, r, int64(len(b)), minio.PutObjectOptions{
		ContentType:  contentType,
		StorageClass: s.conf.StorageClass,
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", objectPath, err)
	}
	return nil
}

// uploadArchive uploads a snapshot archive to S3.
func (s *s3Repository) uploadArchive(ctx context.Context, snapshotID string, archive *SnapshotArchive) error {
	// Open archive file.
	f, err := os.Open(archive.path)
	if err != nil {
		return fmt.Errorf("open snapshot archive: %w", err)
	}
	defer f.Close()

	// Read file stat for file size.
	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat snapshot archive: %w", err)
	}
	fileSize := stat.Size()

	// Upload the archive file to S3.
	objectPath := filepath.Join(s.conf.Prefix, snapshotID, archive.Name)
	_, err = s.minioClient.PutObject(ctx, s.conf.Bucket, objectPath, f, fileSize, minio.PutObjectOptions{
		ContentType:  archive.compression.contentType(),
		StorageClass: s.conf.StorageClass,
	})
	if err != nil {
		return fmt.Errorf("upload snapshot archive (%s): %w", objectPath, err)
	}

	return nil
}
