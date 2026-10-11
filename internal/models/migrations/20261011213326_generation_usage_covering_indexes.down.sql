DROP INDEX generation_usage_owner_idx;

DROP INDEX generation_usage_owner_purpose_idx;

CREATE INDEX generation_usage_owner_idx ON generation_usage (owner_id, created_at);

CREATE INDEX generation_usage_owner_purpose_idx ON generation_usage (owner_id, purpose, created_at);
