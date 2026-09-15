package main

// engine_file_move.go — the repair for bytes that are in the bucket and in the wrong place
// (ADR 0072 decision 2, follow-up to the split-family parts next door).
//
// 🔴 Two halves of one line of the ingest form put every Anima and Krea 2 row on af-sandbox into
// this state (measured 2026-09-15):
//
//	s3Key = engineIngestPrefix(image, fileFlag, isLora) + file
//
//	1. the role selector's DEFAULT is "the whole checkpoint", and the prefix is read from that
//	   role, so a split family's own weights landed under `image/checkpoints/`;
//	2. `file` is the path inside the upstream repository, and Hugging Face publishes these
//	   families under `split_files/…`, so the key kept that directory as well.
//
// Either half alone is a file no ComfyUI loader can offer: the box mirrors the bucket, each
// loader enumerates ONE directory, and the Agent names a file by its base name. The row then
// looks complete apart from a `--diffusion-model` it is in fact holding, and nothing on the
// screen could move it forward — "揃える" answered `none`, because the family's PART list (the
// encoder and the VAE) was complete.
//
// Both are refused for new ingests now (engine_admin.go's destination check). This is the remedy
// for the rows that already exist, and it is a MOVE rather than a second download: the bytes are
// this deployment's, they are already paid for, and `aws s3 mv` inside one bucket is a
// server-side copy. Re-fetching 13.1 GB to put them 1 directory higher would be the same repair
// the parts table already refused to be.

import (
	"context"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineMainFileFix is one row's misplaced weights: what they should be called, where they are
// and where the loader will look for them.
type engineMainFileFix struct {
	Flag     string
	From, To string
	// The declaration as it stands. The move carries it across unchanged — same bytes, same
	// provenance, same immutable identity — because nothing about the file changes except the
	// two fields above.
	File store.EngineModelFile
}

// engineMainFileFixFor answers "these weights are here, under a name no template reads". False
// for every row there is nothing to say about: a family that reads a whole checkpoint, a LoRA, a
// row that declares no unflagged file, one that already holds the role, and one whose unflagged
// file is somehow already at the right key (nothing to move, and re-labelling is the panel's
// ordinary edit).
func engineMainFileFixFor(role, family string, m store.EngineModel) (engineMainFileFix, bool) {
	flag := engineFamilyMainFlag(family)
	if flag == "" || engineModelIsLora(m) {
		return engineMainFileFix{}, false
	}
	var whole *store.EngineModelFile
	for i, f := range m.Files {
		switch strings.TrimSpace(f.Flag) {
		case "":
			if strings.TrimSpace(f.S3Key) != "" {
				whole = &m.Files[i]
			}
		case flag:
			// The row already reads something as its weights. Whatever the unflagged file is, it
			// is not this row's missing part, and moving it would take the taken slot.
			return engineMainFileFix{}, false
		}
	}
	if whole == nil {
		return engineMainFileFix{}, false
	}
	from := strings.TrimSpace(whole.S3Key)
	to := engineComfyKeyFor(role, flag, from, false)
	if to == from {
		return engineMainFileFix{}, false
	}
	moved := *whole
	moved.Flag, moved.S3Key = flag, to
	return engineMainFileFix{Flag: flag, From: from, To: to, File: moved}, true
}

// engineMainFileFixRow is the fix as the panel and the check answer read it.
func engineMainFileFixRow(fix engineMainFileFix) map[string]any {
	row := map[string]any{"flag": fix.Flag, "from": fix.From, "to": fix.To}
	if fix.File.Bytes > 0 {
		row["bytes"] = fix.File.Bytes
	}
	return row
}

// engineMainFileMovable is every reason a move must not be started, asked before a task is.
// Each one is a state where moving would destroy something: bytes another row reads, a key
// somebody else's job is about to write, or an object that is not there to move.
// Every 409 it answers carries a holder and a next act (ADR 0085 decision 5): what stands in the
// way of a move is always a row, a job or an object, and the Console draws `next` as the button
// on the error line.
func (a engineAdminAPI) engineMainFileMovable(ctx context.Context, held enginePartsHeld,
	role, id string, fix engineMainFileFix) *apiRefusal {
	if held.known == nil {
		// engineStorageRows failed. A move decided without the job history could relocate a key
		// an unfinished upload is about to write, so it is refused rather than guessed at.
		return refuse(http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment's ingest history could not be read, and moving "+fix.From+
				" without it could take bytes another job is writing — try again", nil, &apiNext{Act: "wait"})
	}
	if k := held.known[fix.From]; k != nil {
		for other := range k.ModelIDs {
			if other != id {
				return refuse(http.StatusConflict, errCodeEngineBadBody,
					fix.From+" is also declared by "+other+", and moving it would leave that row"+
						" pointing at a key with nothing in it — give this row its own copy instead",
					&apiHolder{Kind: "row", ID: other, Key: fix.From}, &apiNext{Act: "forget_row", Target: other})
			}
		}
		if k.InFlight {
			return refuse(http.StatusConflict, errCodeEngineBadBody,
				"an ingest is still writing "+fix.From+" — wait for it to finish and press again",
				&apiHolder{Kind: "object", Key: fix.From}, &apiNext{Act: "wait"})
		}
	}
	// The destination, by the same rule every upload obeys: a key another row or another job
	// already names is not a vacant filename.
	if aerr := engineIngestDestinationUnused(ctx, a.mgr.store, a.mgr.store, role, fix.To); aerr != nil {
		return engineRefusalOf(aerr, &apiHolder{Kind: "object", Key: fix.To}, &apiNext{Act: "dismiss_job"})
	}
	// And the bytes themselves. A declaration whose object was purged is a row to take in again,
	// not one to move — and `aws s3 mv` on a key with nothing at it fails minutes later in a
	// Fargate task, which is the shape of report this whole file exists to move earlier.
	if held.storage == nil {
		return refuse(http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment cannot check the bucket right now, so "+fix.From+" was not moved",
			nil, &apiNext{Act: "wait"})
	}
	if check := held.storage.verify(ctx, fix.From); check.State != engineStoragePresent {
		return refuse(http.StatusConflict, errCodeEngineBadBody,
			"there is nothing at "+fix.From+" to move: this row's weights have to be taken in again",
			&apiHolder{Kind: "object", Key: fix.From}, nil)
	}
	return nil
}

// engineStartMainFileMove relocates the bytes and hands the catalogue change to the job, which
// applies it when the task lands (engineIngester.install). No licence is asked for: these bytes
// were accepted when they were taken in, and this press moves them one directory.
func (a engineAdminAPI) engineStartMainFileMove(ctx context.Context, g engineIngestGrant,
	role, id string, fix engineMainFileFix) (store.EngineIngestJob, *apiError) {
	ing := a.reg.ingester()
	if ing == nil {
		return store.EngineIngestJob{}, &apiError{http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment's engine stack declares no ingest task, and the Control Plane may not write to the " +
				"bucket itself — move " + fix.From + " to " + fix.To + " by hand and re-register the row"}
	}
	return ing.start(ctx, engineIngestRequest{
		Role: role, ModelID: id, S3Key: fix.To, MoveFrom: fix.From, FileFlag: fix.Flag,
		AcceptedBy: g.ident.ID, AcceptedTenant: g.tenantID,
		VaeBundled: fix.File.VaeBundled,
		// What is known about these bytes, carried rather than resolved: the upstream is not
		// contacted by a move, and an identity that could not be re-derived (a gated repository,
		// a Civitai version taken down) must not be dropped from a file that still has one.
		Resolved: engineResolved{
			Bytes: fix.File.Bytes, Source: fix.File.Source,
			ArtifactIdentity: fix.File.ArtifactIdentity,
		},
	})
}
