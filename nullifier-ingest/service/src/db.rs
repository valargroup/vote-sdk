use anyhow::Result;
use rusqlite::{Connection, OptionalExtension as _};

/// Create the nullifier-tree schema: `nullifiers` and `checkpoint` tables.
pub fn create_schema(connection: &Connection) -> Result<()> {
    connection.execute(
        "CREATE TABLE IF NOT EXISTS checkpoint(
        height INTEGER NOT NULL)",
        [],
    )?;
    connection.execute(
        "CREATE TABLE IF NOT EXISTS nullifiers(
        height INTEGER NOT NULL,
        nullifier BLOB NOT NULL UNIQUE)",
        [],
    )?;
    connection.execute(
        "CREATE INDEX IF NOT EXISTS idx_nullifiers_height ON nullifiers(height)",
        [],
    )?;
    Ok(())
}

/// Save the last fully-synced block height.
pub fn save_checkpoint(connection: &Connection, height: u64) -> Result<()> {
    connection.execute("DELETE FROM checkpoint", [])?;
    connection.execute(
        "INSERT INTO checkpoint(height) VALUES (?1)",
        [height],
    )?;
    Ok(())
}

/// Load the last checkpoint height, if any.
pub fn load_checkpoint(connection: &Connection) -> Result<Option<u64>> {
    let value = connection
        .query_row("SELECT height FROM checkpoint", [], |r| r.get::<_, u64>(0))
        .optional()?;
    Ok(value)
}

/// Delete all nullifiers at the given height (for re-ingesting the checkpoint block).
pub fn delete_nullifiers_at_height(connection: &Connection, height: u64) -> Result<()> {
    connection.execute(
        "DELETE FROM nullifiers WHERE height = ?1",
        [height],
    )?;
    Ok(())
}
