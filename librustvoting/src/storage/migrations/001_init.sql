CREATE TABLE rounds (
    round_id            TEXT PRIMARY KEY,
    snapshot_height     INTEGER NOT NULL,
    ea_pk               BLOB NOT NULL,
    nc_root             BLOB NOT NULL,
    nullifier_imt_root  BLOB NOT NULL,
    session_json        TEXT,
    phase               INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL,
    gov_comm_rand       BLOB,
    dummy_nullifiers    BLOB,
    rho_signed          BLOB,
    padded_note_data    BLOB
);

CREATE TABLE cached_tree_state (
    round_id        TEXT PRIMARY KEY REFERENCES rounds(round_id),
    snapshot_height INTEGER NOT NULL,
    tree_state      BLOB NOT NULL
);

CREATE TABLE proofs (
    round_id    TEXT PRIMARY KEY REFERENCES rounds(round_id),
    witness     BLOB,
    proof       BLOB,
    success     INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

CREATE TABLE votes (
    id              INTEGER PRIMARY KEY,
    round_id        TEXT NOT NULL REFERENCES rounds(round_id),
    proposal_id     INTEGER NOT NULL,
    choice          INTEGER NOT NULL,
    commitment      BLOB,
    submitted       INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    UNIQUE(round_id, proposal_id)
);
