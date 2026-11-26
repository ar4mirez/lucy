-- 0001: the append-only event log, per-stream versions and projection bookkeeping.
-- Forward-only: never edit this file once released; add a new migration instead.

CREATE TABLE events (
    seq              INTEGER PRIMARY KEY AUTOINCREMENT,
    stream           TEXT    NOT NULL,
    stream_version   INTEGER NOT NULL CHECK (stream_version > 0),
    type             TEXT    NOT NULL,
    type_version     INTEGER NOT NULL CHECK (type_version > 0),
    envelope_version INTEGER NOT NULL CHECK (envelope_version > 0),
    occurred_at      INTEGER NOT NULL, -- Unix nanoseconds, UTC
    actor            TEXT    NOT NULL,
    payload          TEXT    NOT NULL CHECK (json_valid(payload)),
    UNIQUE (stream, stream_version)
) STRICT;

CREATE TRIGGER events_append_only_update BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TRIGGER events_append_only_delete BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TABLE streams (
    id      TEXT    PRIMARY KEY,
    version INTEGER NOT NULL CHECK (version > 0)
) STRICT;

CREATE TABLE projections (
    name     TEXT    PRIMARY KEY,
    version  INTEGER NOT NULL CHECK (version > 0),
    last_seq INTEGER NOT NULL CHECK (last_seq >= 0)
) STRICT;
