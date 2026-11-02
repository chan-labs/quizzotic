-- The effect an applied tag operation had (affected problems and tag counts),
-- recorded when it was applied. NULL for operations that are not applied and
-- for those applied before the effect was recorded.
ALTER TABLE tag_operations ADD COLUMN result JSONB;
