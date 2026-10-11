-- Caps sum the tokens of a window on every submit. Carrying the token counts in the index answers
-- the sum from the index alone, without reading the heap rows it points to.
DROP INDEX generation_usage_owner_idx;

DROP INDEX generation_usage_owner_purpose_idx;

CREATE INDEX generation_usage_owner_idx ON generation_usage (owner_id, created_at) INCLUDE (input_tokens, output_tokens);

CREATE INDEX generation_usage_owner_purpose_idx ON generation_usage (owner_id, purpose, created_at) INCLUDE (input_tokens, output_tokens);
