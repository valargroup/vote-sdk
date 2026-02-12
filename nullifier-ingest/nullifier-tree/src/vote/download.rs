use rusqlite::{params, Connection};
use tonic::Request;

use super::errors::VoteError;
use super::{
    election::Election,
    PoolConnection, Result,
};
use crate::rpc::{BlockId, BlockRange, CompactBlock};
use crate::download::connect_lwd;

pub async fn download_reference_data(
    connection: PoolConnection,
    id_election: u32,
    election: &Election,
    lwd_url: &str,
    progress: impl Fn(u32) + Send + 'static,
) -> Result<(PoolConnection, u32)> {
    const NU5_ACTIVATION_HEIGHT: u64 = 1_687_104;
    let start = std::cmp::max(election.start_height as u64, NU5_ACTIVATION_HEIGHT);
    let end = election.end_height as u64;
    let lwd_url = lwd_url.to_string();

    let task = tokio::spawn(async move {
        let mut client = connect_lwd(&lwd_url).await?;
        let mut blocks = client
            .get_block_range(Request::new(BlockRange {
                start: Some(BlockId {
                    height: start + 1,
                    hash: vec![],
                }),
                end: Some(BlockId {
                    height: end,
                    hash: vec![],
                }),
                spam_filter_threshold: 0,
            }))
            .await?
            .into_inner();
        while let Some(block) = blocks.message().await? {
            let height = block.height as u32;
            if height % 1000 == 0 || height == end as u32 {
                progress(block.height as u32);
            }
            handle_block(
                &connection,
                id_election,
                block,
            )?;
        }

        Ok::<_, VoteError>(connection)
    });

    let connection = tokio::spawn(async move {
        match task.await {
            Ok(Ok(connection)) => Ok(connection),
            Ok(Err(err)) => {
                eprintln!("Task returned an error: {}", err);
                Err(err)
            }
            Err(err) => {
                eprintln!("Task panicked: {:?}", err);
                let e: anyhow::Error = err.into();
                Err(e.into())
            }
        }
    })
    .await
    .unwrap()?;

    Ok((connection, end as u32))
}

fn handle_block(
    connection: &Connection,
    id_election: u32,
    block: CompactBlock,
) -> Result<()> {
    let mut s_nf = connection.prepare_cached("INSERT INTO nfs(election, hash) VALUES (?1, ?2)")?;
    for tx in block.vtx {
        for a in tx.actions {
            let nf = &a.nullifier;
            s_nf.execute(params![id_election, nf])?;
        }
    }

    Ok(())
}
