-- Whether the caller submitted the generation for streaming, so the provider created its call in
-- streaming mode and its events can be read while it runs.
ALTER TABLE generations
ADD COLUMN streamed boolean NOT NULL DEFAULT false;
