-- Storix database schema.
-- Developed by X Project.

CREATE TABLE IF NOT EXISTS users (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    username             TEXT    NOT NULL COLLATE NOCASE,
    display_name         TEXT    NOT NULL DEFAULT '',
    email                TEXT    NOT NULL DEFAULT '',
    password_hash        TEXT    NOT NULL,
    role                 TEXT    NOT NULL DEFAULT 'user',
    permissions          TEXT    NOT NULL DEFAULT '',
    totp_secret          TEXT    NOT NULL DEFAULT '',
    totp_enabled         INTEGER NOT NULL DEFAULT 0,
    active               INTEGER NOT NULL DEFAULT 1,
    must_change_password INTEGER NOT NULL DEFAULT 0,
    theme                TEXT    NOT NULL DEFAULT 'dark',
    locale               TEXT    NOT NULL DEFAULT 'en',
    quota                INTEGER NOT NULL DEFAULT 0,
    failed_logins        INTEGER NOT NULL DEFAULT 0,
    locked_until         INTEGER,
    last_login_at        INTEGER,
    last_login_ip        TEXT    NOT NULL DEFAULT '',
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users(username COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS roots (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    path       TEXT    NOT NULL UNIQUE,
    label      TEXT    NOT NULL DEFAULT '',
    icon       TEXT    NOT NULL DEFAULT 'folder',
    read_only  INTEGER NOT NULL DEFAULT 0,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS user_mounts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path       TEXT    NOT NULL,
    label      TEXT    NOT NULL DEFAULT '',
    icon       TEXT    NOT NULL DEFAULT 'folder',
    read_only  INTEGER NOT NULL DEFAULT 0,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE(user_id, path)
);
CREATE INDEX IF NOT EXISTS idx_user_mounts_user ON user_mounts(user_id);

CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf         TEXT    NOT NULL,
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS shares (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    token          TEXT    NOT NULL UNIQUE,
    owner_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path           TEXT    NOT NULL,
    name           TEXT    NOT NULL DEFAULT '',
    kind           TEXT    NOT NULL DEFAULT 'download',
    is_dir         INTEGER NOT NULL DEFAULT 0,
    password_hash  TEXT    NOT NULL DEFAULT '',
    allow_download INTEGER NOT NULL DEFAULT 1,
    allow_upload   INTEGER NOT NULL DEFAULT 0,
    allow_list     INTEGER NOT NULL DEFAULT 1,
    max_downloads  INTEGER NOT NULL DEFAULT 0,
    downloads      INTEGER NOT NULL DEFAULT 0,
    note           TEXT    NOT NULL DEFAULT '',
    expires_at     INTEGER,
    last_access_at INTEGER,
    created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_shares_owner ON shares(owner_id);

CREATE TABLE IF NOT EXISTS trash_items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL,
    name          TEXT    NOT NULL,
    original_path TEXT    NOT NULL,
    stored_path   TEXT    NOT NULL,
    is_dir        INTEGER NOT NULL DEFAULT 0,
    size          INTEGER NOT NULL DEFAULT 0,
    deleted_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_trash_user ON trash_items(user_id, deleted_at DESC);

CREATE TABLE IF NOT EXISTS favorites (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path       TEXT    NOT NULL,
    name       TEXT    NOT NULL DEFAULT '',
    is_dir     INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    UNIQUE(user_id, path)
);

CREATE TABLE IF NOT EXISTS recents (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path    TEXT    NOT NULL,
    name    TEXT    NOT NULL DEFAULT '',
    is_dir  INTEGER NOT NULL DEFAULT 0,
    size    INTEGER NOT NULL DEFAULT 0,
    action  TEXT    NOT NULL DEFAULT 'open',
    at      INTEGER NOT NULL,
    UNIQUE(user_id, path)
);
CREATE INDEX IF NOT EXISTS idx_recents_user ON recents(user_id, at DESC);

CREATE TABLE IF NOT EXISTS audit_log (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id  INTEGER NOT NULL DEFAULT 0,
    username TEXT    NOT NULL DEFAULT '',
    action   TEXT    NOT NULL,
    target   TEXT    NOT NULL DEFAULT '',
    detail   TEXT    NOT NULL DEFAULT '',
    ip       TEXT    NOT NULL DEFAULT '',
    ua       TEXT    NOT NULL DEFAULT '',
    ok       INTEGER NOT NULL DEFAULT 1,
    at       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_at ON audit_log(at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_log(user_id, at DESC);

CREATE TABLE IF NOT EXISTS uploads (
    id          TEXT    PRIMARY KEY,
    user_id     INTEGER NOT NULL DEFAULT 0,
    share_token TEXT    NOT NULL DEFAULT '',
    target_dir  TEXT    NOT NULL,
    filename    TEXT    NOT NULL,
    rel_path    TEXT    NOT NULL DEFAULT '',
    size        INTEGER NOT NULL DEFAULT 0,
    offset_bytes INTEGER NOT NULL DEFAULT 0,
    temp_path   TEXT    NOT NULL,
    metadata    TEXT    NOT NULL DEFAULT '',
    overwrite   INTEGER NOT NULL DEFAULT 0,
    completed   INTEGER NOT NULL DEFAULT 0,
    final_path  TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_uploads_user ON uploads(user_id);
CREATE INDEX IF NOT EXISTS idx_uploads_expires ON uploads(expires_at);

CREATE TABLE IF NOT EXISTS jobs (
    id           TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL DEFAULT 0,
    type         TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'queued',
    title        TEXT    NOT NULL DEFAULT '',
    message      TEXT    NOT NULL DEFAULT '',
    error        TEXT    NOT NULL DEFAULT '',
    total        INTEGER NOT NULL DEFAULT 0,
    done         INTEGER NOT NULL DEFAULT 0,
    total_items  INTEGER NOT NULL DEFAULT 0,
    done_items   INTEGER NOT NULL DEFAULT 0,
    params       TEXT    NOT NULL DEFAULT '',
    result       TEXT    NOT NULL DEFAULT '',
    cancellable  INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    started_at   INTEGER,
    finished_at  INTEGER
);
CREATE INDEX IF NOT EXISTS idx_jobs_user ON jobs(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS login_attempts (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    ip       TEXT    NOT NULL,
    username TEXT    NOT NULL DEFAULT '',
    ok       INTEGER NOT NULL DEFAULT 0,
    at       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_login_attempts_ip ON login_attempts(ip, at DESC);
