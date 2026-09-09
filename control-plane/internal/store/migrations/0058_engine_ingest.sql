-- Ingest jobs, and who accepted a model's licence (ADR 0072 decisions 6 and 10, phase P4).
--
-- Taking a model in is a Fargate task that runs for minutes: 18.5 GB from Hugging Face took
-- 163 s on one measurement and 35 s on another, and the same file can take 12 minutes when HF
-- is slow (4-236 MB/s, measured — ADR 0071 decision 3). That is far longer than a request, so
-- the job cannot live in the request that started it, and it must not live only in memory
-- either: a Control Plane replaced mid-download would otherwise leave a task running with
-- nobody watching and no row to show for it.
--
-- ⚠️ This table is the ONLY record that a job exists. The task itself is reconciled from ECS
-- (DescribeTasks) rather than trusted from here, so a row that says `running` while ECS says
-- the task is gone is repaired on the next poll — the opposite direction (believing this table)
-- would show a download that finished hours ago as still going.
CREATE TABLE engine_ingest_jobs (
  id            TEXT PRIMARY KEY,            -- the job id the panel polls
  role          TEXT NOT NULL,               -- engine key the model is for
  model_id      TEXT NOT NULL,               -- catalogue id the row will be created with
  s3_key        TEXT NOT NULL,               -- where in the bucket it lands
  source        TEXT NOT NULL DEFAULT '',    -- human-readable origin (hf:<repo>/<file>, url, civitai:<id>)
  task_arn      TEXT NOT NULL DEFAULT '',    -- the ECS task, once RunTask returned
  state         TEXT NOT NULL,               -- pending | running | done | failed
  -- Why it failed, in the words the task used. Empty while it is going well, and the panel
  -- shows it verbatim rather than an exit code: "sha256 mismatch" and "401 on a gated
  -- repository" need completely different things from the person reading it.
  -- ⚠️ No semicolon anywhere in a comment in this file. The migration runner splits the file
  -- on semicolons with no SQL parser, so one inside a comment cuts a statement in half and the
  -- Control Plane stops booting with "incomplete input" (measured, twice, including here).
  message       TEXT NOT NULL DEFAULT '',
  bytes         INTEGER NOT NULL DEFAULT 0,  -- declared size, for the row and for "sync +N s"
  -- The catalogue row this job will create, as JSON, written when the job starts. It lives
  -- here rather than in the process that started it because a Control Plane can be replaced
  -- mid-download: the bytes would land in the bucket and the row nobody could reconstruct.
  spec          TEXT NOT NULL DEFAULT '',
  started_by    TEXT NOT NULL DEFAULT '',    -- identity id of the super_admin who asked
  created_at    TEXT NOT NULL DEFAULT '',
  updated_at    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_engine_ingest_jobs_state ON engine_ingest_jobs(state);

-- Who accepted the licence, and whether this deployment may use the model commercially.
--
-- ⚠️ Both are records of a HUMAN act, not of a file's contents. A gated repository is one whose
-- owner distributes only to accounts that accepted its terms, and in a multi-tenant deployment
-- the operator accepts on behalf of every member (ADR 0072 decision 10) — so who did it and
-- when is exactly what an audit of that decision needs, and neither can be reconstructed from
-- the model card afterwards.
ALTER TABLE engine_models ADD COLUMN license_accepted_by TEXT NOT NULL DEFAULT '';
ALTER TABLE engine_models ADD COLUMN license_accepted_at TEXT NOT NULL DEFAULT '';
-- yes | no | unknown -- resolved from the licence at ingest, never guessed later. "no" is what
-- makes a non-commercial model (FLUX.1-dev, Kontext) visibly wrong for a deployment that
-- charges its members, which is a rule about the DEPLOYMENT that the licence name alone does
-- not say out loud.
ALTER TABLE engine_models ADD COLUMN commercial_use TEXT NOT NULL DEFAULT '';
