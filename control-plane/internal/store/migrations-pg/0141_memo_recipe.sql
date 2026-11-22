-- Message queues, a run recipe a message carries, Postgres mirror of migrations/0156_memo_recipe.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN IF NOT EXISTS recipe TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN IF NOT EXISTS release_recipe TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR release_recipe = recipe)
