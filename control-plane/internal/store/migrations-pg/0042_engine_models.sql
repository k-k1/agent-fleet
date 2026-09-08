-- The engine model catalogue (ADR 0072 decision 2).
--
-- What a self-hosted engine loads used to be a CloudFormation parameter -- LlmModelS3Key,
-- ImageModelFile and ten siblings -- so swapping a checkpoint meant editing params/60-engines
-- and running a stack update. It is an OPERATIONAL act, not a deployment one: an administrator
-- picks another checkpoint at night while the GPU is asleep, and CloudFormation is not the
-- control anyone reaches for. This table is where that declaration moves to. The stack keeps
-- the VESSEL (capacity provider, service, bucket, task definition). The catalogue says what
-- goes in it.
--
-- ⚠️ A TABLE, not a JSON blob in `settings`. There are two writers -- the administrator's
-- toggle and the ingest job's state transitions -- and SettingsStore reads the whole value and
-- writes the whole value back with no compare-and-swap, so two concurrent writes lose one of
-- them. The list, the seed and the delete are all row operations, and `files` / `args` /
-- `sizes` differ in shape from row to row (ADR 0072 open question 5).
--
-- ⚠️ There is deliberately NO last_used_at column. It would mean an UPDATE on the request path
-- for a figure nobody decides anything on. When it is wanted it goes in behind the same
-- one-minute throttle `engine_<key>_demand_at` uses (ADR 0072 open question 5, phase P4).
--
-- ⚠️ `license` and `license_name` are BOTH kept, and dropping either loses the fact. Hugging
-- Face reports `cardData.license = "other"` for FLUX.1-dev and SD 3.5, with the real terms in
-- `license_name` (`flux-1-dev-non-commercial-license`, `stabilityai-ai-community`) -- so a
-- catalogue that copies only the first shows the two NON-COMMERCIAL models as "other".
-- The values are a SNAPSHOT taken at ingest: the model card can change under a deployment
-- that already accepted the terms.
CREATE TABLE engine_models (
  role              TEXT NOT NULL,               -- 'llm' / 'image' -- the ENGINE KEY this model is for
  id                TEXT NOT NULL,               -- what a member picks: 'sdxl-base-1.0'
  kind              TEXT NOT NULL DEFAULT '',    -- gguf | checkpoint | lora | vae | text_encoder | diffusion_model
  files             TEXT NOT NULL DEFAULT '[]',  -- JSON [{role,s3Key,file}] -- >1 entry = a split model
  enabled           INTEGER NOT NULL DEFAULT 0,  -- synced onto the box and offered (ingest creates rows OFF)
  selected          INTEGER NOT NULL DEFAULT 0,  -- image: the ONE checkpoint sd-server starts with
  is_default        INTEGER NOT NULL DEFAULT 0,  -- llm: the model a request with no `model` gets
  args              TEXT NOT NULL DEFAULT '[]',  -- JSON [] -- per-model flags (--vae, --type q8_0)
  context_tokens    INTEGER NOT NULL DEFAULT 0,  -- llm: this model's window (0 = not declared)
  max_output_tokens INTEGER NOT NULL DEFAULT 0,
  sizes             TEXT NOT NULL DEFAULT '[]',  -- image: JSON [] of WIDTHxHEIGHT, DECLARED not guessed
  description       TEXT NOT NULL DEFAULT '',    -- one line an AGENT reads when it chooses
  vram_mib          INTEGER NOT NULL DEFAULT 0,  -- the operator's own measurement, 0 = unmeasured
  license           TEXT NOT NULL DEFAULT '',    -- HF cardData.license, verbatim ('other' is a real value)
  license_name      TEXT NOT NULL DEFAULT '',    -- where the terms actually are when license='other'
  license_url       TEXT NOT NULL DEFAULT '',
  -- `model_precision`, not `precision`: PRECISION is a SQL keyword (DOUBLE PRECISION) and a
  -- column name that only some dialects accept is a CP that boots on SQLite and not on RDS.
  model_precision   TEXT NOT NULL DEFAULT '',    -- fp16 | fp8 | q8_0 | q4_k -- 12B+ needs quantising on an L4
  base_model        TEXT NOT NULL DEFAULT '',    -- sdxl | sd35 | flux1 | flux2-klein | zimage -- a LoRA's fit
  created_at        TEXT NOT NULL DEFAULT '',
  updated_at        TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (role, id)
);
CREATE INDEX idx_engine_models_role_enabled ON engine_models(role, enabled);
