//! Field element serialization and validation helpers.
//!
//! Provides utilities for reading, writing, and validating Pasta Fp field
//! elements in 32-byte little-endian encoding, plus binary search over
//! record arrays keyed by Fp values.

use ff::PrimeField as _;
use pasta_curves::Fp;

/// Write an Fp value as 32 little-endian bytes into `buf`.
#[inline]
pub fn write_fp(buf: &mut [u8], fp: Fp) {
    buf[..32].copy_from_slice(&fp.to_repr());
}

/// Read an Fp value from 32 little-endian bytes.
///
/// # Panics
///
/// Panics if the encoding is non-canonical. Callers must ensure the data
/// has been validated with [`validate_all_fp_chunks`] before calling this.
#[inline]
pub fn read_fp(buf: &[u8]) -> Fp {
    let mut arr = [0u8; 32];
    arr.copy_from_slice(&buf[..32]);
    Fp::from_repr(arr).expect("read_fp: non-canonical Fp (caller must validate first)")
}

/// Validate that a 32-byte slice is a canonical Fp encoding.
#[inline]
pub fn validate_fp_bytes(buf: &[u8]) -> anyhow::Result<()> {
    anyhow::ensure!(
        buf.len() == 32,
        "invalid field element byte length: got {}, expected 32",
        buf.len()
    );
    let mut arr = [0u8; 32];
    arr.copy_from_slice(buf);
    let fp = Fp::from_repr(arr);
    anyhow::ensure!(
        bool::from(fp.is_some()),
        "non-canonical field element encoding"
    );
    Ok(())
}

/// Validate that every 32-byte chunk in `data` is a canonical Fp encoding.
pub fn validate_all_fp_chunks(data: &[u8], tier_label: &str) -> anyhow::Result<()> {
    for (i, chunk) in data.chunks_exact(32).enumerate() {
        validate_fp_bytes(chunk).map_err(|e| {
            anyhow::anyhow!("{} invalid field element at 32-byte chunk {}: {}", tier_label, i, e)
        })?;
    }
    Ok(())
}

/// Binary search a sequence of fixed-size records for the last entry whose key <= `value`.
///
/// Each record has a 32-byte Fp key at `key_offset` bytes within the record.
/// `base` is the byte offset in `data` where the record array starts.
/// `count` is the number of records, `record_size` the byte size of each.
///
/// Returns `Some(index)` of the last record with key <= value, or `None` if all keys > value.
pub fn binary_search_records(
    data: &[u8],
    base: usize,
    count: usize,
    record_size: usize,
    key_offset: usize,
    value: Fp,
) -> Option<usize> {
    let mut lo = 0usize;
    let mut hi = count;
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        let k_off = base + mid * record_size + key_offset;
        let k = read_fp(&data[k_off..k_off + 32]);
        if k <= value {
            lo = mid + 1;
        } else {
            hi = mid;
        }
    }
    if lo == 0 { None } else { Some(lo - 1) }
}
