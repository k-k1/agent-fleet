package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// egressArchiveSink is where the egress archive objects go. Put is complete-or-nothing: when
// it returns nil the whole object is stored and readable, and when it fails no object, and
// no partial one, is left under key. The purge deletes the rows an object holds as soon as
// Put returns nil, so an implementation that cannot say "confirmed" must return an error.
// Keys are slash-separated and relative to the sink's own root; a sink for another store
// (GCS, say) implements these two methods and nothing else changes.
type egressArchiveSink interface {
	Exists(ctx context.Context, key string) (bool, error)
	// Get opens an object that Exists reported, so it can be verified before it is trusted.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Put stores size bytes read from body, which is seekable so a client may retry or sign it.
	Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error
}

// newEgressArchiveSink builds the sink AF_EGRESS_ARCHIVE_URL names: s3://bucket[/prefix] or
// file:///absolute/dir.
func newEgressArchiveSink(ctx context.Context, raw string) (egressArchiveSink, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("AF_EGRESS_ARCHIVE_URL: %w", err)
	}
	switch u.Scheme {
	case "file":
		return newEgressArchiveLocal(u.Host, u.Path)
	case "s3":
		if u.Host == "" {
			return nil, errors.New("AF_EGRESS_ARCHIVE_URL: s3:// needs a bucket")
		}
		region := firstEnv("AF_ECS_REGION", "AWS_REGION", "AWS_DEFAULT_REGION")
		ac, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
		if err != nil {
			return nil, fmt.Errorf("AF_EGRESS_ARCHIVE_URL: aws config: %w", err)
		}
		return newEgressArchiveS3(s3.NewFromConfig(ac), u.Host, u.Path), nil
	}
	return nil, fmt.Errorf("AF_EGRESS_ARCHIVE_URL: want s3://bucket/prefix or file:///dir, got scheme %q", u.Scheme)
}

// egressArchiveLocal writes under a directory: a temporary file in the target's own
// directory (so the rename stays on one filesystem), fsync, rename, then fsync of the
// directory so the rename itself survives a crash.
type egressArchiveLocal struct{ root string }

func newEgressArchiveLocal(host, dir string) (*egressArchiveLocal, error) {
	if host != "" || !filepath.IsAbs(dir) {
		return nil, errors.New("AF_EGRESS_ARCHIVE_URL: file:// wants an absolute path (file:///var/lib/af/archive)")
	}
	return &egressArchiveLocal{root: filepath.Clean(dir)}, nil
}

// path joins key under root. Keys are built here, never from outside; the check keeps a
// future caller from writing beside the root.
func (l *egressArchiveLocal) path(key string) (string, error) {
	p := filepath.Join(l.root, filepath.FromSlash(key))
	if p != l.root && !strings.HasPrefix(p, l.root+string(filepath.Separator)) {
		return "", fmt.Errorf("archive key %q leaves the archive directory", key)
	}
	return p, nil
}

func (l *egressArchiveLocal) Exists(_ context.Context, key string) (bool, error) {
	p, err := l.path(key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (l *egressArchiveLocal) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// mkdirAllSync creates dir and every missing ancestor, and fsyncs each directory it created
// together with its parent, so the whole path to a file renamed into it survives a crash
// (an fsync of the leaf alone does not persist the entries above it).
func mkdirAllSync(dir string) error {
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
		return nil
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if err := mkdirAllSync(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	return syncDir(parent)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (l *egressArchiveLocal) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) (err error) {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := mkdirAllSync(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".egress-archive-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	n, err := io.Copy(f, &ctxReader{ctx, body})
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("archive %s: copied %d of %d bytes", key, n, size)
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(f.Name(), 0o644); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), p); err != nil {
		return err
	}
	return syncDir(dir)
}

// ctxReader stops a copy when the purge's deadline (the lease) has passed.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

type egressArchiveS3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// egressArchiveS3 stores objects under bucket/prefix. A single PutObject replaces the key
// atomically, so a reader sees the old object or the whole new one. The task role needs
// s3:PutObject and s3:GetObject (HeadObject and read-back) on the prefix, and s3:ListBucket on
// the bucket: without it S3 answers HeadObject for a missing key with 403, not 404, which
// Exists must (and does) report as an error rather than as "absent". The CP never lists,
// overwrites a good object, or deletes.
type egressArchiveS3 struct {
	bucket, prefix string
	api            egressArchiveS3API
}

func newEgressArchiveS3(api egressArchiveS3API, bucket, prefix string) *egressArchiveS3 {
	return &egressArchiveS3{bucket: bucket, prefix: strings.Trim(prefix, "/"), api: api}
}

func (s *egressArchiveS3) key(k string) string {
	if s.prefix == "" {
		return k
	}
	return path.Join(s.prefix, k)
}

func (s *egressArchiveS3) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(key))})
	if err == nil {
		return true, nil
	}
	if engineStorageNotFound(err) {
		return false, nil
	}
	return false, err
}

func (s *egressArchiveS3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.key(key))})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

// Put uploads, then asks the bucket for the object's length: a 2xx that did not store what
// was sent must not authorise a delete.
func (s *egressArchiveS3) Put(ctx context.Context, key string, body io.ReadSeeker, size int64) error {
	k := s.key(key)
	if _, err := s.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(k), Body: body, ContentLength: aws.Int64(size),
		ContentType: aws.String("application/gzip"),
	}); err != nil {
		return err
	}
	out, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(k)})
	if err != nil {
		return fmt.Errorf("archive %s not confirmed: %w", key, err)
	}
	if got := aws.ToInt64(out.ContentLength); got != size {
		return fmt.Errorf("archive %s not confirmed: stored %d bytes, sent %d", key, got, size)
	}
	return nil
}
