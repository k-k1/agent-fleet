package store

import "testing"

// EngineIngestJobForS3Key is the write-side fence: a key some job already addresses must not be
// handed to a second upload. What it must NOT do is fence off a key nothing was ever written
// to — which is what a job that never got a task is.
//
// 🔴 Measured on af-sandbox 2026-09-15. Three repairs were refused by RunTask
// (`TaskDefinition is inactive`), and the three job rows they left behind then refused the
// retry: "the S3 key … is already recorded". The cause was fixed, the press could not be.
func TestEngineIngestJobForS3KeyIgnoresAJobThatNeverStarted(t *testing.T) {
	st := engineModelStore(t)
	ctx := t.Context()
	put := func(key, state, arn string) {
		t.Helper()
		if err := st.PutEngineIngestJob(ctx, EngineIngestJob{
			ID: NewID(), Role: "image", ModelID: "m", S3Key: key, State: state, TaskArn: arn,
			Message: "operation error ECS: RunTask", CreatedAt: NowTS(), UpdatedAt: NowTS(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	recorded := func(key string) bool {
		t.Helper()
		_, got, err := st.EngineIngestJobForS3Key(ctx, "image", key)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	put("image/diffusion_models/never-ran.safetensors", EngineIngestFailed, "")
	if recorded("image/diffusion_models/never-ran.safetensors") {
		t.Error("a job that RunTask refused still holds its destination")
	}
	// Everything else is still an address. A task that ran may have uploaded before it failed,
	// and a pending one is about to.
	put("image/diffusion_models/ran-then-failed.safetensors", EngineIngestFailed, "arn:aws:ecs:x:1:task/c/a1")
	if !recorded("image/diffusion_models/ran-then-failed.safetensors") {
		t.Error("a failed task's destination was freed, and its upload container may have run")
	}
	put("image/diffusion_models/pending.safetensors", EngineIngestPending, "")
	if !recorded("image/diffusion_models/pending.safetensors") {
		t.Error("a job waiting for its task no longer holds its destination")
	}
	put("image/diffusion_models/done.safetensors", EngineIngestDone, "arn:aws:ecs:x:1:task/c/a2")
	if !recorded("image/diffusion_models/done.safetensors") {
		t.Error("a finished upload no longer holds its destination")
	}
	// The role is part of the question: two roles' prefixes are separate.
	if _, got, err := st.EngineIngestJobForS3Key(ctx, "llm", "image/diffusion_models/done.safetensors"); err != nil || got {
		t.Errorf("another role saw this key = (%v,%v)", got, err)
	}
}

// The refusal this feeds has to NAME the job, so the id it hands back is the one an operator can
// dismiss — and when a key has been written more than once, the newest of them: the older rows are
// history, and dismissing one of those would not free anything.
func TestEngineIngestJobForS3KeyNamesTheNewestJob(t *testing.T) {
	st := engineModelStore(t)
	ctx := t.Context()
	const key = "image/diffusion_models/twice.safetensors"
	for _, j := range []EngineIngestJob{
		{ID: "older", CreatedAt: "2026-09-01T00:00:00Z"},
		{ID: "newest", CreatedAt: "2026-09-16T00:00:00Z"},
	} {
		j.Role, j.ModelID, j.S3Key = "image", "m", key
		j.State, j.TaskArn = EngineIngestDone, "arn:aws:ecs:x:1:task/c/"+j.ID
		j.UpdatedAt = j.CreatedAt
		if err := st.PutEngineIngestJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	got, found, err := st.EngineIngestJobForS3Key(ctx, "image", key)
	if err != nil || !found {
		t.Fatalf("the key is recorded twice: found=%v err=%v", found, err)
	}
	if got.ID != "newest" {
		t.Fatalf("job = %q, want the newest of the two", got.ID)
	}
	if got.State != EngineIngestDone || got.TaskArn == "" {
		t.Errorf("the row came back without the fields the refusal reads: %+v", got)
	}
}
