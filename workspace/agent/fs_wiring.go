package main

// fs_wiring.go — wires `internal/fsx`'s outward dependencies (fsx → main) in one place.
//
// Never give the wiring defaults: `fsx.Configure` panics on anything left unwired, because an
// empty code would reach the Console as a blank error code.

import "github.com/k-k1/agent-fleet/workspace/agent/internal/fsx"

func init() { fsx.Configure(fsDeps()) }

func fsDeps() fsx.Deps {
	return fsx.Deps{
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
}
