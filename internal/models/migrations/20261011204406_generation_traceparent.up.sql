-- The W3C trace context of the request that submitted the generation. A check that runs it later, in
-- another trace, links back to that request. Null for a submission made outside any trace.
ALTER TABLE generations
ADD COLUMN traceparent text;
