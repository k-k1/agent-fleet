package main

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type fakeEngineStorageS3 struct {
	in  *s3.HeadObjectInput
	out *s3.HeadObjectOutput
	err error
	// The listing, as the bucket would page it: one entry per call, the last of them untruncated.
	pages   []*s3.ListObjectsV2Output
	listIn  []*s3.ListObjectsV2Input
	listErr error
	// The ranged read: what the object holds, what the call was asked with, and a refusal.
	body    []byte
	getIn   *s3.GetObjectInput
	getErr  error
	getBody io.ReadCloser
}

func (f *fakeEngineStorageS3) GetObject(_ context.Context, in *s3.GetObjectInput,
	_ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.getIn = in
	if f.getErr != nil {
		return nil, f.getErr
	}
	body := f.getBody
	if body == nil {
		body = io.NopCloser(bytes.NewReader(f.body))
	}
	return &s3.GetObjectOutput{Body: body}, nil
}

func (f *fakeEngineStorageS3) HeadObject(_ context.Context, in *s3.HeadObjectInput,
	_ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.in = in
	return f.out, f.err
}

func (f *fakeEngineStorageS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input,
	_ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listIn = append(f.listIn, in)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.pages) == 0 {
		return &s3.ListObjectsV2Output{}, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}

// 🔴 The pagination is the half a single-page test would never reach, and a bucket that outgrows
// one page is the normal end state for a deployment that takes models in. A continuation token
// that is not sent back means a ledger silently missing its tail — and the act the ledger offers
// on an object nothing declares is deletion.
func TestEngineAWSStorageListFollowsTheContinuationToken(t *testing.T) {
	api := &fakeEngineStorageS3{pages: []*s3.ListObjectsV2Output{
		{
			Contents: []s3types.Object{
				{Key: aws.String("image/checkpoints/a.safetensors"), Size: aws.Int64(10)},
				// A console-made directory placeholder: not an object anybody can load, register
				// or delete, so not a ledger row either.
				{Key: aws.String("image/vae/"), Size: aws.Int64(0)},
			},
			IsTruncated: aws.Bool(true), NextContinuationToken: aws.String("page-2"),
		},
		{Contents: []s3types.Object{{Key: aws.String("image/vae/b.safetensors"), Size: aws.Int64(20)}}},
	}}
	got, err := newEngineAWSStorageMetadata("models", api).List(t.Context(), "image/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Key != "image/checkpoints/a.safetensors" || got[1].Key != "image/vae/b.safetensors" {
		t.Fatalf("objects = %+v, want both pages and no directory placeholder", got)
	}
	if len(api.listIn) != 2 || aws.ToString(api.listIn[1].ContinuationToken) != "page-2" {
		t.Fatalf("the second call did not carry the token: %+v", api.listIn)
	}
	if aws.ToString(api.listIn[0].Prefix) != "image/" || aws.ToString(api.listIn[0].Bucket) != "models" {
		t.Errorf("list input = %+v", api.listIn[0])
	}
}

// A listing that was refused is an ERROR and never an empty bucket: the ledger's caller draws an
// empty answer as "nothing is stored here", and the act on an object nothing declares is 消す.
func TestEngineAWSStorageListReportsRefusalRatherThanEmptiness(t *testing.T) {
	api := &fakeEngineStorageS3{listErr: &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}}
	if _, err := newEngineAWSStorageMetadata("models", api).List(t.Context(), "image/"); err == nil {
		t.Fatal("a refused listing answered no error")
	}
	if _, err := newEngineAWSStorageMetadata("", nil).List(t.Context(), "image/"); err == nil {
		t.Fatal("an unconfigured bucket answered no error")
	}
}

func TestEngineAWSStorageMetadataClassifiesHeadObject(t *testing.T) {
	for _, tc := range []struct {
		name  string
		out   *s3.HeadObjectOutput
		err   error
		state string
	}{
		{name: "present", out: &s3.HeadObjectOutput{ContentLength: aws.Int64(42), ChecksumSHA256: aws.String("sum"), VersionId: aws.String("v1")}, state: engineStoragePresent},
		{name: "missing", err: &smithy.GenericAPIError{Code: "NotFound", Message: "not found"}, state: engineStorageMissing},
		{name: "denied", err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}, state: engineStorageUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeEngineStorageS3{out: tc.out, err: tc.err}
			got := newEngineAWSStorageMetadata("models", api).Stat(t.Context(), "image/models/a.safetensors")
			if got.State != tc.state {
				t.Fatalf("state = %q, want %q", got.State, tc.state)
			}
			if api.in == nil || aws.ToString(api.in.Bucket) != "models" ||
				aws.ToString(api.in.Key) != "image/models/a.safetensors" || api.in.ChecksumMode != s3types.ChecksumModeEnabled {
				t.Fatalf("HeadObject input = %+v", api.in)
			}
			if tc.name == "present" && (got.Bytes != 42 || got.ChecksumSHA256 != "sum" || got.VersionID != "v1") {
				t.Errorf("present metadata = %+v", got)
			}
		})
	}
}

// The ranged read has to ASK for a range. Without the header S3 answers 200 with the whole
// object, and the objects this is pointed at are checkpoints of several gigabytes on the shared
// CP task — the LimitReader is what stops it, but only after the transfer has started.
func TestEngineAWSStoragePrefixAsksForARangeAndStopsAtTheWindow(t *testing.T) {
	api := &fakeEngineStorageS3{body: []byte("0123456789")}
	got, err := newEngineAWSStorageMetadata("models", api).Prefix(t.Context(), "image/checkpoints/a.safetensors", 4)
	if err != nil {
		t.Fatalf("Prefix: %v", err)
	}
	if string(got) != "0123" {
		t.Fatalf("prefix = %q, want the first 4 bytes", got)
	}
	if api.getIn == nil || aws.ToString(api.getIn.Bucket) != "models" ||
		aws.ToString(api.getIn.Key) != "image/checkpoints/a.safetensors" ||
		aws.ToString(api.getIn.Range) != "bytes=0-3" {
		t.Fatalf("GetObject input = %+v", api.getIn)
	}
}

// A server that ignored the Range must not turn a header read into a multi-gigabyte download.
func TestEngineAWSStoragePrefixCapsAServerThatIgnoredTheRange(t *testing.T) {
	api := &fakeEngineStorageS3{body: bytes.Repeat([]byte("x"), 4096)}
	got, err := newEngineAWSStorageMetadata("models", api).Prefix(t.Context(), "image/checkpoints/a.safetensors", 16)
	if err != nil || len(got) != 16 {
		t.Fatalf("prefix = %d bytes, err = %v; want the window", len(got), err)
	}
}

// A refused read is an ERROR and never an empty prefix: an empty buffer parses as "this is not a
// safetensors file", which for a checkpoint that is merely unreadable would be a verdict invented
// out of a permission failure.
func TestEngineAWSStoragePrefixReportsRefusalRatherThanEmptiness(t *testing.T) {
	api := &fakeEngineStorageS3{getErr: &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}}
	if _, err := newEngineAWSStorageMetadata("models", api).Prefix(t.Context(), "image/checkpoints/a.safetensors", 16); err == nil {
		t.Fatal("a refused read answered no error")
	}
	if _, err := newEngineAWSStorageMetadata("", nil).Prefix(t.Context(), "image/checkpoints/a.safetensors", 16); err == nil {
		t.Fatal("an unconfigured bucket answered no error")
	}
	if _, err := newEngineAWSStorageMetadata("models", api).Prefix(t.Context(), "image/checkpoints/a.safetensors", 0); err == nil {
		t.Fatal("a zero-byte window answered no error")
	}
}

func TestEngineAWSStorageMetadataWithoutConfigurationIsUnknown(t *testing.T) {
	if got := newEngineAWSStorageMetadata("", nil).Stat(t.Context(), "image/models/a"); got.State != engineStorageUnknown {
		t.Fatalf("unconfigured AWS metadata = %+v", got)
	}
}
