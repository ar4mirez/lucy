-- 0002: consumer cursors with generation-bound acks (LUCY-EVT-003).
-- Forward-only: never edit this file once released; add a new migration instead.

CREATE TABLE consumers (
    name       TEXT    PRIMARY KEY,
    generation INTEGER NOT NULL CHECK (generation > 0),
    cursor_seq INTEGER NOT NULL CHECK (cursor_seq >= 0),
    updated_at INTEGER NOT NULL -- Unix nanoseconds, UTC
) STRICT;
