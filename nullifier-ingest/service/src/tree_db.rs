use anyhow::Result;
use ff::PrimeField as _;
use pasta_curves::Fp;
use rusqlite::Connection;

use imt_tree::{build_nf_ranges, NullifierTree, Range};

/// Load all nullifiers from the database, sort them, and build the gap ranges.
pub fn list_nf_ranges(connection: &Connection) -> Result<Vec<Range>> {
    let mut s = connection.prepare("SELECT nullifier FROM nullifiers")?;
    let rows = s.query_map([], |r| {
        let v = r.get::<_, [u8; 32]>(0)?;
        let v = Fp::from_repr(v).unwrap();
        Ok(v)
    })?;
    let mut nfs = rows.collect::<Result<Vec<_>, _>>()?;
    nfs.sort();
    Ok(build_nf_ranges(nfs))
}

/// Build a NullifierTree directly from the database.
pub fn tree_from_db(connection: &Connection) -> Result<NullifierTree> {
    let ranges = list_nf_ranges(connection)?;
    Ok(NullifierTree::from_ranges(ranges))
}
