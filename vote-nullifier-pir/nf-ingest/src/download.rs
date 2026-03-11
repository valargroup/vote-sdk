use anyhow::Result;
use tonic::transport::{Channel, ClientTlsConfig};

use crate::rpc::compact_tx_streamer_client::CompactTxStreamerClient;

/// Create a connected `CompactTxStreamerClient` for the given lightwalletd URL.
///
/// If the URL starts with `https`, TLS is configured using the default root
/// certificates provided by the `tls-roots` feature (webpki-roots / Mozilla bundle).
pub async fn connect_lwd(lwd_url: &str) -> Result<CompactTxStreamerClient<Channel>> {
    let mut ep = Channel::from_shared(lwd_url.to_owned())?;
    if lwd_url.starts_with("https") {
        ep = ep.tls_config(ClientTlsConfig::new())?;
    }
    let client = CompactTxStreamerClient::connect(ep).await?;
    Ok(client)
}
