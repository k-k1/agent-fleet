package main

// memory_wiring.go — wires `internal/memoryx`'s outward dependencies (memoryx → main) in
// one place.
//
// The opposite direction (main → memoryx) needs no file: main names memoryx.X directly.
//
// Never give the wiring defaults. `memoryx.Configure` panics on anything left unwired.
// Everything here is a string, so an accepted zero value would send the code `""` to the
// Console, i18n could not resolve it, and the raw developer message would be exposed — a
// silent breakage.

import "github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"

func init() { memoryx.Configure(memoryDeps()) }

// memoryDeps is the production wiring. memoryx's own exhaustiveness check
// (internal/memoryx/deps_test.go) uses fakes, so this is the only place the real values are
// written.
func memoryDeps() memoryx.Deps {
	return memoryx.Deps{
		ErrCodeBadRequest:     errCodeMemoryBadRequest,
		ErrCodeBadRev:         errCodeMemoryBadRev,
		ErrCodeBadPath:        errCodeMemoryBadPath,
		ErrCodeNoSnapshots:    errCodeMemoryNoSnapshots,
		ErrCodeSnapshotFailed: errCodeMemorySnapshotFailed,
		ErrCodeDiffFailed:     errCodeMemoryDiffFailed,
		ErrCodeBadScope:       errCodeMemoryBadScope,
		ErrCodeRestoreFailed:  errCodeMemoryRestoreFailed,
		ErrCodeExportFailed:   errCodeMemoryExportFailed,
		ErrCodeImportFailed:   errCodeMemoryImportFailed,
		ErrCodeBadImport:      errCodeMemoryBadImport,
		ErrCodeSecretDetected: errCodeMemorySecretDetected,
		ErrCodeTooLarge:       errCodeMemoryTooLarge,
		ErrCodeNotFound:       errCodeMemoryNotFound,
		ErrCodeConflict:       errCodeMemoryConflict,
		ErrCodeNoProject:      errCodeMemoryNoProject,
		ErrCodeDisabled:       errCodeMemoryDisabled,
	}
}
