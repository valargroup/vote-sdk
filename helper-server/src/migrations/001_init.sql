PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS shares (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    round_id        TEXT    NOT NULL,
    share_index     INTEGER NOT NULL,

    -- Full payload (JSON blob for all_enc_shares, individual fields for the rest)
    shares_hash     TEXT    NOT NULL,
    proposal_id     INTEGER NOT NULL,
    vote_decision   INTEGER NOT NULL,
    enc_share_c1    TEXT    NOT NULL,
    enc_share_c2    TEXT    NOT NULL,
    tree_position   INTEGER NOT NULL,
    all_enc_shares  TEXT    NOT NULL,   -- JSON array of {c1, c2, share_index}

    -- Processing state
    state           INTEGER NOT NULL DEFAULT 0,  -- 0=Received, 1=Witnessed, 2=Submitted, 3=Failed
    attempts        INTEGER NOT NULL DEFAULT 0,

    UNIQUE(round_id, share_index)
);
