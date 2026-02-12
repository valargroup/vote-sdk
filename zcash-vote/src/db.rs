use anyhow::Result;
use rusqlite::{params, Connection};

pub fn create_schema(connection: &Connection) -> Result<()> {
    connection.execute(
        "CREATE TABLE IF NOT EXISTS ballots(
        id_ballot INTEGER PRIMARY KEY,
        election INTEGER NOT NULL,
        height INTEGER NOT NULL,
        hash BLOB NOT NULL UNIQUE,
        data BLOB NOT NULL)",
        [],
    )?;
    connection.execute(
        "CREATE TABLE IF NOT EXISTS dnfs(
        id_dnf INTEGER PRIMARY KEY NOT NULL,
        election INTEGER NOT NULL,
        hash BLOB NOT NULL UNIQUE)",
        [],
    )?;
    Ok(())
}

pub fn store_dnf(connection: &Connection, id_election: u32, dnf: &[u8]) -> Result<()> {
    connection.execute(
        "INSERT INTO dnfs(election, hash) VALUES (?1, ?2)",
        params![id_election, dnf],
    )?;
    Ok(())
}
