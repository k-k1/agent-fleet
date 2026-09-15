package main

// engine_storage_aws.go — AWS implementation of the object metadata port.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type engineStorageS3API interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// engineStorageListPages bounds one listing. The CP task role's `s3:ListBucket` is already
// conditioned on the `llm/*` and `image/*` prefixes (60-engines.yaml), so the ceiling is not a
// permission but a memory one: a bucket that somehow grew a million objects must not turn one
// panel load into a million rows in the CP's heap. 20 pages is 20,000 objects against a measured
// bucket of under a hundred.
const engineStorageListPages = 20

type engineAWSStorageMetadata struct {
	bucket string
	api    engineStorageS3API
}

func newEngineAWSStorageMetadata(bucket string, api engineStorageS3API) *engineAWSStorageMetadata {
	return &engineAWSStorageMetadata{bucket: strings.TrimSpace(bucket), api: api}
}

func (a *engineAWSStorageMetadata) Stat(ctx context.Context, key string) engineStorageObjectMetadata {
	if a == nil || a.api == nil || a.bucket == "" {
		return engineStorageObjectMetadata{State: engineStorageUnknown}
	}
	out, err := a.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(a.bucket), Key: aws.String(key), ChecksumMode: s3types.ChecksumModeEnabled,
	})
	if err == nil {
		return engineStorageObjectMetadata{
			State: engineStoragePresent, Bytes: aws.ToInt64(out.ContentLength),
			ChecksumSHA256: aws.ToString(out.ChecksumSHA256), VersionID: aws.ToString(out.VersionId),
		}
	}
	if engineStorageNotFound(err) {
		return engineStorageObjectMetadata{State: engineStorageMissing}
	}
	// AccessDenied, missing credentials, timeouts and every unclassified failure are unknown.
	// Treating an inability to ask as absence would offer a download that overwrites real bytes.
	return engineStorageObjectMetadata{State: engineStorageUnknown}
}

// List pages through the prefix. Every failure is returned rather than classified: unlike
// HeadObject, where "the bucket says no such key" is an answer the catalogue needs, a listing
// that was refused has no honest partial form — half a ledger reads as "the rest is gone".
func (a *engineAWSStorageMetadata) List(ctx context.Context, prefix string) ([]engineStorageObject, error) {
	if a == nil || a.api == nil || a.bucket == "" {
		return nil, errEngineStorageUnconfigured
	}
	var (
		out   []engineStorageObject
		token *string
	)
	for page := 0; page < engineStorageListPages; page++ {
		res, err := a.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(a.bucket), Prefix: aws.String(prefix), ContinuationToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, o := range res.Contents {
			key := aws.ToString(o.Key)
			if key == "" || strings.HasSuffix(key, "/") {
				// A zero-byte "directory" placeholder a console created. It is not an object
				// anybody can load, register or delete, so it is not a ledger row either.
				continue
			}
			obj := engineStorageObject{Key: key, Bytes: aws.ToInt64(o.Size)}
			if o.LastModified != nil {
				obj.LastModified = *o.LastModified
			}
			out = append(out, obj)
		}
		if !aws.ToBool(res.IsTruncated) || aws.ToString(res.NextContinuationToken) == "" {
			return out, nil
		}
		token = res.NextContinuationToken
	}
	// Said out loud rather than silently truncated: a ledger missing its tail is one an operator
	// would act on, and the act ("nothing declares this, delete it") is irreversible.
	return out, fmt.Errorf("the bucket lists more than %d objects under %q", engineStorageListPages*1000, prefix)
}

func engineStorageNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey", "NoSuchObject":
			return true
		}
	}
	var responseErr *smithyhttp.ResponseError
	return errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == http.StatusNotFound
}
