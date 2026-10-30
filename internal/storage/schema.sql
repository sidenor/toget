CREATE TABLE servers (
    id          TEXT PRIMARY KEY,
    code        TEXT NOT NULL UNIQUE,
    public_key  BLOB NOT NULL,
    private_key BLOB NOT NULL,
    nonce       BLOB NOT NULL,
    manifest    TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE TABLE accounts (
    id            TEXT PRIMARY KEY,
    server_id     TEXT NOT NULL REFERENCES servers(id),
    code          TEXT NOT NULL,
    name          TEXT NOT NULL,
    public_key    BLOB NOT NULL,
    private_key   BLOB NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    UNIQUE(server_id, code)
);

CREATE TABLE devices (
    id          TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL REFERENCES accounts(id),
    name        TEXT NOT NULL,
    public_key  BLOB NOT NULL,
    private_key BLOB NOT NULL,
    certificate TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE INDEX devices_account ON devices(account_id);

CREATE TABLE rooms (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    home_node  TEXT NOT NULL,
    created_by TEXT NOT NULL,
    epoch      INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE events (
    id         TEXT PRIMARY KEY,
    room_id    TEXT NOT NULL REFERENCES rooms(id),
    seq        INTEGER NOT NULL,
    type       TEXT NOT NULL,
    author_id  TEXT NOT NULL,
    payload    BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    prev_id    TEXT NOT NULL DEFAULT '',
    hash       TEXT NOT NULL,
    UNIQUE(room_id, seq)
);

CREATE INDEX events_room_seq ON events(room_id, seq);

CREATE TRIGGER events_no_update
BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TRIGGER events_no_delete
BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;
