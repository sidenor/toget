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
    id                       TEXT PRIMARY KEY,
    account_id               TEXT NOT NULL REFERENCES accounts(id),
    name                     TEXT NOT NULL,
    public_key               BLOB NOT NULL,
    private_key              BLOB NOT NULL,
    encryption_public_key    BLOB NOT NULL DEFAULT x'',
    encryption_private_key   BLOB NOT NULL DEFAULT x'',
    certificate              TEXT NOT NULL,
    role                     TEXT NOT NULL DEFAULT 'node',
    revoked_at               INTEGER,
    last_seen_at             INTEGER,
    created_at               INTEGER NOT NULL,
    local                    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX devices_account ON devices(account_id);

CREATE TABLE rooms (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    topic           TEXT NOT NULL DEFAULT '',
    home_node       TEXT NOT NULL,
    home_server_id  TEXT NOT NULL DEFAULT '',
    home_address    TEXT NOT NULL DEFAULT '',
    created_by      TEXT NOT NULL,
    epoch           INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    archived_at     INTEGER
);

CREATE TABLE room_members (
    room_id              TEXT NOT NULL REFERENCES rooms(id),
    account_id           TEXT NOT NULL,
    device_id            TEXT NOT NULL DEFAULT '',
    display_name         TEXT NOT NULL DEFAULT '',
    role                 TEXT NOT NULL DEFAULT 'member',
    status               TEXT NOT NULL DEFAULT 'active',
    account_public_key   BLOB NOT NULL DEFAULT x'',
    encryption_public_key BLOB NOT NULL DEFAULT x'',
    joined_at            INTEGER NOT NULL,
    left_at              INTEGER,
    PRIMARY KEY (room_id, account_id)
);

CREATE INDEX room_members_account ON room_members(account_id);

CREATE TABLE room_devices (
    room_id               TEXT NOT NULL REFERENCES rooms(id),
    device_id             TEXT NOT NULL,
    account_id            TEXT NOT NULL,
    public_key            BLOB NOT NULL,
    encryption_public_key BLOB NOT NULL DEFAULT x'',
    joined_at             INTEGER NOT NULL,
    revoked_at            INTEGER,
    PRIMARY KEY (room_id, device_id)
);

CREATE TABLE room_epochs (
    room_id     TEXT NOT NULL REFERENCES rooms(id),
    epoch       INTEGER NOT NULL,
    key_sealed  TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (room_id, epoch)
);

CREATE TABLE room_member_keys (
    room_id     TEXT NOT NULL REFERENCES rooms(id),
    epoch       INTEGER NOT NULL,
    device_id   TEXT NOT NULL,
    wrapped_key BLOB NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (room_id, epoch, device_id)
);

CREATE TABLE events (
    id            TEXT PRIMARY KEY,
    room_id       TEXT NOT NULL REFERENCES rooms(id),
    seq           INTEGER NOT NULL,
    epoch         INTEGER NOT NULL DEFAULT 1,
    type          TEXT NOT NULL,
    author_id     TEXT NOT NULL,
    author_device TEXT NOT NULL DEFAULT '',
    payload       BLOB NOT NULL,
    created_at    INTEGER NOT NULL,
    prev_id       TEXT NOT NULL DEFAULT '',
    prev_hash     TEXT NOT NULL DEFAULT '',
    hash          TEXT NOT NULL,
    signature     TEXT NOT NULL DEFAULT '',
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

CREATE TABLE agents (
    id            TEXT PRIMARY KEY,
    account_id    TEXT NOT NULL,
    name          TEXT NOT NULL,
    runtime       TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'idle',
    permissions   TEXT NOT NULL DEFAULT '{}',
    created_at    INTEGER NOT NULL,
    last_seen_at  INTEGER,
    revoked_at    INTEGER
);

CREATE INDEX agents_account ON agents(account_id);

CREATE TABLE agent_rooms (
    agent_id    TEXT NOT NULL REFERENCES agents(id),
    room_id     TEXT NOT NULL REFERENCES rooms(id),
    joined_at   INTEGER NOT NULL,
    PRIMARY KEY (agent_id, room_id)
);

CREATE TABLE agent_sessions (
    agent_id        TEXT NOT NULL REFERENCES agents(id),
    room_id         TEXT NOT NULL REFERENCES rooms(id),
    last_seen_seq   INTEGER NOT NULL DEFAULT 0,
    last_run_seq    INTEGER NOT NULL DEFAULT 0,
    active_event_id TEXT NOT NULL DEFAULT '',
    active_seq      INTEGER NOT NULL DEFAULT 0,
    runtime_session TEXT NOT NULL DEFAULT '',
    run_count       INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'idle',
    last_error      TEXT NOT NULL DEFAULT '',
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (agent_id, room_id)
);

CREATE INDEX agent_sessions_room ON agent_sessions(room_id);

CREATE TABLE approvals (
    id             TEXT PRIMARY KEY,
    room_id        TEXT NOT NULL REFERENCES rooms(id),
    agent_id       TEXT NOT NULL,
    requested_by   TEXT NOT NULL,
    action         TEXT NOT NULL,
    action_digest  TEXT NOT NULL,
    description    TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    expires_at     INTEGER NOT NULL,
    decided_by     TEXT,
    decided_at     INTEGER,
    consumed_at    INTEGER,
    created_at     INTEGER NOT NULL
);

CREATE INDEX approvals_room_status ON approvals(room_id, status);

CREATE TABLE tool_receipts (
    id           TEXT PRIMARY KEY,
    room_id      TEXT NOT NULL REFERENCES rooms(id),
    agent_id     TEXT NOT NULL,
    approval_id  TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL,
    tool         TEXT NOT NULL,
    input        TEXT NOT NULL DEFAULT '{}',
    output       TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL,
    created_at   INTEGER NOT NULL
);

CREATE INDEX tool_receipts_room ON tool_receipts(room_id, created_at);

CREATE TABLE attachments (
    id          TEXT PRIMARY KEY,
    room_id     TEXT NOT NULL REFERENCES rooms(id),
    event_id    TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL,
    mime        TEXT NOT NULL,
    size        INTEGER NOT NULL,
    sha256      TEXT NOT NULL,
    path        TEXT NOT NULL,
    key_sealed  TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE TABLE read_cursors (
    room_id    TEXT NOT NULL REFERENCES rooms(id),
    device_id  TEXT NOT NULL,
    last_seq   INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (room_id, device_id)
);

CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    target       TEXT NOT NULL,
    kind         TEXT NOT NULL,
    payload      BLOB NOT NULL,
    attempts     INTEGER NOT NULL DEFAULT 0,
    next_attempt INTEGER NOT NULL,
    created_at   INTEGER NOT NULL
);

CREATE TABLE peers (
    server_id  TEXT PRIMARY KEY,
    code       TEXT NOT NULL DEFAULT '',
    address    TEXT NOT NULL,
    public_key BLOB NOT NULL DEFAULT x'',
    last_seen  INTEGER NOT NULL
);

CREATE TABLE invites (
    id          TEXT PRIMARY KEY,
    room_id     TEXT NOT NULL REFERENCES rooms(id),
    secret_hash TEXT NOT NULL UNIQUE,
    created_by  TEXT NOT NULL,
    history     TEXT NOT NULL DEFAULT 'none',
    expires_at  INTEGER NOT NULL,
    used_at     INTEGER,
    created_at  INTEGER NOT NULL
);

CREATE TABLE policies (
    id         TEXT PRIMARY KEY,
    room_id    TEXT NOT NULL REFERENCES rooms(id),
    agent_id   TEXT NOT NULL DEFAULT '',
    rules      TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
