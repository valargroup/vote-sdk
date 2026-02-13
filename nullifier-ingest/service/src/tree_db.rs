use anyhow::Result;
use ff::PrimeField as _;
use pasta_curves::Fp;
use rusqlite::Connection;

use imt_tree::{build_sentinel_tree, NullifierTree};

/// Load all nullifiers from the database.
fn load_all_nullifiers(connection: &Connection) -> Result<Vec<Fp>> {
    let mut s = connection.prepare("SELECT nullifier FROM nullifiers")?;
    let rows = s.query_map([], |r| {
        let v = r.get::<_, [u8; 32]>(0)?;
        let v = Fp::from_repr(v).unwrap();
        Ok(v)
    })?;
    Ok(rows.collect::<Result<Vec<_>, _>>()?)
}

/// Build a NullifierTree from the database, merging sentinel nullifiers.
///
/// The delegation circuit's q_interval gate range-checks interval widths to
/// < 2^250. Sentinel nullifiers at k * 2^250 (k = 0..=16) partition the Pallas
/// field so that every gap range stays within this bound.
pub fn tree_from_db(connection: &Connection) -> Result<NullifierTree> {
    let nfs = load_all_nullifiers(connection)?;
    Ok(build_sentinel_tree(&nfs))
}
