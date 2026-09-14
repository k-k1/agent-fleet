package main

import (
	"context"
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
}

func (f *fakeEngineStorageS3) HeadObject(_ context.Context, in *s3.HeadObjectInput,
	_ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.in = in
	return f.out, f.err
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

func TestEngineAWSStorageMetadataWithoutConfigurationIsUnknown(t *testing.T) {
	if got := newEngineAWSStorageMetadata("", nil).Stat(t.Context(), "image/models/a"); got.State != engineStorageUnknown {
		t.Fatalf("unconfigured AWS metadata = %+v", got)
	}
}
