-- Every submit records its generation's start intent since v0.9.0, and the sweep settled the
-- generations earlier releases left without one, which the purge has since deleted. The constraint
-- states that invariant, so a check never needs a branch for a missing start. A row without a start
-- fails the migration before any release relies on it. The scan holds the table's lock briefly: the
-- purge keeps the table to a few hours of generations.
ALTER TABLE generations
ALTER COLUMN start_requested_at
SET NOT NULL;
