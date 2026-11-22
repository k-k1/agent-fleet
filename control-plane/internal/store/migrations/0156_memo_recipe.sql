-- Message queues, a run recipe a message carries (ADR 0137, addendum 2026-11-23, #4845).
-- memo.recipe is the JSON object {"agentKind","model","effort","route"} a message asks its run to
-- use in place of the category's settings, field by field, and empty for none. Content, like
-- start_after: the person or the session that wrote the message set it, and a release is bound to it.
-- release_recipe copies the recipe a pending message was released with, and the CHECK refuses any
-- write that leaves a pending message with another recipe, as the CHECKs of 0131, 0138, 0144 and 0152
-- do for the rest of the content. So a Control Plane that predates this migration cannot leave a
-- pending row under a release of another recipe.
-- Forward compatible: columns with defaults that mean today's behaviour. A binary that predates
-- them runs every message on the category's settings, and an old row reads as a message with no recipe.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN recipe TEXT NOT NULL DEFAULT '';
ALTER TABLE memo ADD COLUMN release_recipe TEXT NOT NULL DEFAULT '' CHECK (state <> 'pending' OR release_recipe = recipe)
