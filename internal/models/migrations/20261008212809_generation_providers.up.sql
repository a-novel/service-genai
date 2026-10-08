-- The provider configuration a generation's attempts run on. Raised to a replica's own epoch when it
-- starts or restarts an attempt, never lowered, so a replica on an older configuration leaves the
-- generation alone and two configurations running side by side during a rollout never restart each
-- other's calls.
ALTER TABLE generations
ADD COLUMN provider_epoch integer CHECK (provider_epoch >= 1);

-- Attempts started before epochs were recorded ran under the first one.
UPDATE generations
SET
  provider_epoch = 1
WHERE
  start_requested_at IS NOT NULL;

ALTER TABLE generations
ADD CONSTRAINT generations_started_with_epoch CHECK (
  start_requested_at IS NULL
  OR provider_epoch IS NOT NULL
);
