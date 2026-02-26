//! Binary serialization for shard trees and checkpoints.
//!
//! Shared between [`crate::kv_shard_store`] and any future persistence layer.
//!
//! # Shard format
//!
//! ```text
//! [version: u8 = 1]
//! [tree: node]
//!
//! node :=
//!   0x00                                -- Nil
//!   0x01 [hash: 32 bytes] [flags: u8]   -- Leaf
//!   0x02 [has_ann: u8] [hash: 32 bytes if has_ann=1] [left: node] [right: node]  -- Parent
//! ```
//!
//! # Checkpoint format
//!
//! ```text
//! [has_position: u8]  [position: u64 LE if has_position=1]
//! [marks_count: u32 LE]  [mark_position: u64 LE] × marks_count
//! ```

use std::collections::BTreeSet;
use std::io::{self, Cursor, Read, Write};
use std::ops::Deref;
use std::sync::Arc;

use incrementalmerkletree::Position;
use shardtree::{
    store::{Checkpoint, TreeState},
    Node, PrunableTree, RetentionFlags, Tree,
};

use crate::hash::MerkleHashVote;

const SHARD_SER_VERSION: u8 = 1;
const NODE_NIL: u8 = 0;
const NODE_LEAF: u8 = 1;
const NODE_PARENT: u8 = 2;

fn write_hash<W: Write>(w: &mut W, h: &MerkleHashVote) -> io::Result<()> {
    w.write_all(&h.to_bytes())
}

fn write_node<W: Write>(w: &mut W, tree: &PrunableTree<MerkleHashVote>) -> io::Result<()> {
    match tree.deref() {
        Node::Parent { ann, left, right } => {
            w.write_all(&[NODE_PARENT])?;
            match ann.as_ref() {
                None => w.write_all(&[0u8])?,
                Some(h) => {
                    w.write_all(&[1u8])?;
                    write_hash(w, h)?;
                }
            }
            write_node(w, left)?;
            write_node(w, right)?;
            Ok(())
        }
        Node::Leaf { value } => {
            w.write_all(&[NODE_LEAF])?;
            write_hash(w, &value.0)?;
            w.write_all(&[value.1.bits()])?;
            Ok(())
        }
        Node::Nil => {
            w.write_all(&[NODE_NIL])?;
            Ok(())
        }
    }
}

/// Serialize a `PrunableTree<MerkleHashVote>` to a versioned blob.
pub fn write_shard_vote(tree: &PrunableTree<MerkleHashVote>) -> io::Result<Vec<u8>> {
    let mut buf = Vec::new();
    buf.push(SHARD_SER_VERSION);
    write_node(&mut buf, tree)?;
    Ok(buf)
}

fn read_hash<R: Read>(r: &mut R) -> io::Result<MerkleHashVote> {
    let mut bytes = [0u8; 32];
    r.read_exact(&mut bytes)?;
    MerkleHashVote::from_bytes(&bytes)
        .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "invalid MerkleHashVote"))
}

fn read_node<R: Read>(r: &mut R) -> io::Result<PrunableTree<MerkleHashVote>> {
    let mut tag = [0u8; 1];
    r.read_exact(&mut tag)?;
    match tag[0] {
        NODE_NIL => Ok(Tree::empty()),
        NODE_LEAF => {
            let hash = read_hash(r)?;
            let mut flag = [0u8; 1];
            r.read_exact(&mut flag)?;
            let flags = RetentionFlags::from_bits_truncate(flag[0]);
            Ok(Tree::leaf((hash, flags)))
        }
        NODE_PARENT => {
            let mut ann_flag = [0u8; 1];
            r.read_exact(&mut ann_flag)?;
            let ann = if ann_flag[0] == 1 {
                Some(Arc::new(read_hash(r)?))
            } else {
                None
            };
            let left = read_node(r)?;
            let right = read_node(r)?;
            Ok(Tree::parent(ann, left, right))
        }
        t => Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("unknown node tag: {t}"),
        )),
    }
}

/// Deserialize a shard blob produced by [`write_shard_vote`].
pub fn read_shard_vote(data: &[u8]) -> io::Result<PrunableTree<MerkleHashVote>> {
    let mut cur = Cursor::new(data);
    let mut version = [0u8; 1];
    cur.read_exact(&mut version)?;
    if version[0] != SHARD_SER_VERSION {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("unknown shard version: {}", version[0]),
        ));
    }
    read_node(&mut cur)
}

/// Serialize a [`Checkpoint`] to bytes.
pub fn write_checkpoint(cp: &Checkpoint) -> Vec<u8> {
    let mut buf = Vec::new();
    match cp.position() {
        None => buf.push(0u8),
        Some(pos) => {
            buf.push(1u8);
            buf.extend_from_slice(&u64::from(pos).to_le_bytes());
        }
    }
    let marks: Vec<u64> = cp.marks_removed().iter().map(|p| u64::from(*p)).collect();
    let count = marks.len() as u32;
    buf.extend_from_slice(&count.to_le_bytes());
    for m in marks {
        buf.extend_from_slice(&m.to_le_bytes());
    }
    buf
}

/// Deserialize a checkpoint blob produced by [`write_checkpoint`].
pub fn read_checkpoint(data: &[u8]) -> io::Result<Checkpoint> {
    let mut cur = Cursor::new(data);

    let mut flag = [0u8; 1];
    cur.read_exact(&mut flag)?;
    let tree_state = if flag[0] == 0 {
        TreeState::Empty
    } else {
        let mut pos_bytes = [0u8; 8];
        cur.read_exact(&mut pos_bytes)?;
        TreeState::AtPosition(Position::from(u64::from_le_bytes(pos_bytes)))
    };

    let mut count_bytes = [0u8; 4];
    cur.read_exact(&mut count_bytes)?;
    let count = u32::from_le_bytes(count_bytes) as usize;

    let mut marks = BTreeSet::new();
    for _ in 0..count {
        let mut pos_bytes = [0u8; 8];
        cur.read_exact(&mut pos_bytes)?;
        marks.insert(Position::from(u64::from_le_bytes(pos_bytes)));
    }
    Ok(Checkpoint::from_parts(tree_state, marks))
}
