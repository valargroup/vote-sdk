CREATE TABLE rounds (
    round_id            TEXT PRIMARY KEY,
    snapshot_height     INTEGER NOT NULL,
    ea_pk               BLOB NOT NULL,
    nc_root             BLOB NOT NULL,
    nullifier_imt_root  BLOB NOT NULL,
    session_json        TEXT,
    phase               INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL
);

CREATE TABLE bundles (
    round_id            TEXT NOT NULL REFERENCES rounds(round_id) ON DELETE CASCADE,
    bundle_index        INTEGER NOT NULL,
    note_positions_blob BLOB,
    van_comm_rand       BLOB,
    dummy_nullifiers    BLOB,
    rho_signed          BLOB,
    padded_note_data    BLOB,
    nf_signed           BLOB,
    cmx_new             BLOB,
    alpha               BLOB,
    rseed_signed        BLOB,
    rseed_output        BLOB,
    gov_comm            BLOB,
    total_note_value    INTEGER,
    address_index       INTEGER,
    van_leaf_position   INTEGER,
    rk                  BLOB,
    gov_nullifiers_blob BLOB,
    PRIMARY KEY (round_id, bundle_index)
);

CREATE TABLE cached_tree_state (
    round_id        TEXT PRIMARY KEY REFERENCES rounds(round_id) ON DELETE CASCADE,
    snapshot_height INTEGER NOT NULL,
    tree_state      BLOB NOT NULL
);

CREATE TABLE proofs (
    round_id      TEXT NOT NULL,
    bundle_index  INTEGER NOT NULL,
    witness       BLOB,
    proof         BLOB,
    success       INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    PRIMARY KEY (round_id, bundle_index),
    FOREIGN KEY (round_id, bundle_index) REFERENCES bundles(round_id, bundle_index) ON DELETE CASCADE
);

CREATE TABLE witnesses (
    round_id        TEXT NOT NULL,
    bundle_index    INTEGER NOT NULL,
    note_position   INTEGER NOT NULL,
    note_commitment BLOB NOT NULL,
    root            BLOB NOT NULL,
    auth_path       BLOB NOT NULL,
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (round_id, bundle_index, note_position),
    FOREIGN KEY (round_id, bundle_index) REFERENCES bundles(round_id, bundle_index) ON DELETE CASCADE
);

CREATE TABLE votes (
    id              INTEGER PRIMARY KEY,
    round_id        TEXT NOT NULL,
    bundle_index    INTEGER NOT NULL,
    proposal_id     INTEGER NOT NULL,
    choice          INTEGER NOT NULL,
    commitment      BLOB,
    submitted       INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    UNIQUE(round_id, bundle_index, proposal_id),
    FOREIGN KEY (round_id, bundle_index) REFERENCES bundles(round_id, bundle_index) ON DELETE CASCADE
);
