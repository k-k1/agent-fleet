package main

// One check that fs_wiring.go is live: fsx.Configure only catches an unwired field, not one
// wired to the wrong constant. Each field is compared by value against errcodes.go.

import (
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fsx"
)

func TestFSWiringIsLive(t *testing.T) {
	w := fsx.Wired()
	want := fsx.Deps{
		ErrCodeFSBadPath:            errCodeFSBadPath,
		ErrCodeFSBadRequest:         errCodeFSBadRequest,
		ErrCodeFSBinaryNotSupported: errCodeFSBinaryNotSupported,
		ErrCodeFSDenied:             errCodeFSDenied,
		ErrCodeFSNotFile:            errCodeFSNotFile,
		ErrCodeFSReadFailed:         errCodeFSReadFailed,
		ErrCodeFSRevisionConflict:   errCodeFSRevisionConflict,
		ErrCodeFSSymlinkNotAllowed:  errCodeFSSymlinkNotAllowed,
		ErrCodeFSTooLarge:           errCodeFSTooLarge,
		ErrCodeFSUnsupportedMedia:   errCodeFSUnsupportedMedia,
		ErrCodeFSUnsupportedNewline: errCodeFSUnsupportedNewline,
		ErrCodeFSWriteCancelled:     errCodeFSWriteCancelled,
		ErrCodeFSWriteFailed:        errCodeFSWriteFailed,
		ErrCodeFSWriteStateUnknown:  errCodeFSWriteStateUnknown,
		ErrCodeTitleFeatureDisabled: errCodeTitleFeatureDisabled,
		ErrCodeZipBusy:              errCodeZipBusy,
		ErrCodeZipChanged:           errCodeZipChanged,
		ErrCodeZipNoSpace:           errCodeZipNoSpace,
		ErrCodeZipNotDir:            errCodeZipNotDir,
		ErrCodeZipTooLarge:          errCodeZipTooLarge,
		ErrCodeZipUnsafe:            errCodeZipUnsafe,
	}
	if w != want {
		t.Errorf("fsx wiring differs from errcodes.go:\n got %+v\nwant %+v", w, want)
	}
}
