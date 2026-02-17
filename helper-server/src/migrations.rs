//! SQLite schema migrations for the helper server share store.

use rusqlite::Connection;

const CURRENT_VERSION: u32 = 1;

pub fn migrate(conn: &Connection) -> Result<(), rusqlite::Error> {
    let version: u32 = conn.pragma_query_value(None, "user_version", |r| r.get(0))?;

    if version < 1 {
        conn.execute_batch(include_str!("migrations/001_init.sql"))?;
        conn.pragma_update(None, "user_version", 1)?;
    }

    let final_version: u32 = conn.pragma_query_value(None, "user_version", |r| r.get(0))?;

    assert_eq!(
        final_version, CURRENT_VERSION,
        "unexpected database version after migration: expected {}, got {}",
        CURRENT_VERSION, final_version
    );

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn migrate_fresh_database() {
        let conn = Connection::open_in_memory().unwrap();
        migrate(&conn).unwrap();

        let version: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap();
        assert_eq!(version, CURRENT_VERSION);
    }

    #[test]
    fn migrate_idempotent() {
        let conn = Connection::open_in_memory().unwrap();
        migrate(&conn).unwrap();
        migrate(&conn).unwrap();

        let version: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap();
        assert_eq!(version, CURRENT_VERSION);
    }

    #[test]
    fn shares_table_created() {
        let conn = Connection::open_in_memory().unwrap();
        migrate(&conn).unwrap();

        let tables: Vec<String> = conn
            .prepare("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
            .unwrap()
            .query_map([], |row| row.get(0))
            .unwrap()
            .collect::<Result<_, _>>()
            .unwrap();

        assert!(tables.contains(&"shares".to_string()));
    }
}
