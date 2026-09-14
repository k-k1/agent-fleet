package main

// engine_storage_aws.go — AWS implementation of the object metadata port.

import (
	"context"
	"errors"
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
}

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
