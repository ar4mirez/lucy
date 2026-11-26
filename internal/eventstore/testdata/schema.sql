-- table consumers
CREATE TABLE consumers (
    name       TEXT    PRIMARY KEY,
    generation INTEGER NOT NULL CHECK (generation > 0),
    cursor_seq INTEGER NOT NULL CHECK (cursor_seq >= 0),
    updated_at INTEGER NOT NULL -- Unix nanoseconds, UTC
) STRICT;

-- table events
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

-- table projections
CREATE TABLE projections (
    name     TEXT    PRIMARY KEY,
    version  INTEGER NOT NULL CHECK (version > 0),
    last_seq INTEGER NOT NULL CHECK (last_seq >= 0)
) STRICT;

-- table schema_migrations
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY CHECK (version > 0),
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT;

-- table streams
CREATE TABLE streams (
    id      TEXT    PRIMARY KEY,
    version INTEGER NOT NULL CHECK (version > 0)
) STRICT;

-- trigger events_append_only_delete
CREATE TRIGGER events_append_only_delete BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;

-- trigger events_append_only_update
CREATE TRIGGER events_append_only_update BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;

