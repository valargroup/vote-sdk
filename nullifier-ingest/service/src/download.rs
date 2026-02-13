use anyhow::Result;
use tonic::transport::{Certificate, Channel, ClientTlsConfig};

use crate::rpc::compact_tx_streamer_client::CompactTxStreamerClient;

/// Create a connected `CompactTxStreamerClient` for the given lightwalletd URL.
///
/// If the URL starts with `https`, TLS is configured using the embedded CA certificate.
pub async fn connect_lwd(lwd_url: &str) -> Result<CompactTxStreamerClient<Channel>> {
    let mut ep = Channel::from_shared(lwd_url.to_owned())?;
    if lwd_url.starts_with("https") {
        let pem = include_bytes!("ca.pem");
        let ca = Certificate::from_pem(pem);
        let tls = ClientTlsConfig::new().ca_certificate(ca);
        ep = ep.tls_config(tls)?;
    }
    let client = CompactTxStreamerClient::connect(ep).await?;
    Ok(client)
}
