-- When the current attempt's first start was requested. A start the provider turns away is retried
-- for a while from then: measured from the submission instead, an attempt requeued after a long call,
-- or one held through a downtime, got no retry at all. A requeue clears it for the next attempt.
ALTER TABLE generations
ADD COLUMN first_start_requested_at timestamp with time zone;
