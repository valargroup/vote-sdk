use vote_commitment_tree_client::transport::{Transport, TransportError, TransportResponse};

/// ReqwestTreeTransport adapts the e2e crate's blocking reqwest client to the
/// transport trait used by vote-commitment-tree-client.
pub struct ReqwestTreeTransport;

impl Transport for ReqwestTreeTransport {
    fn get(&self, url: &str) -> Result<TransportResponse, TransportError> {
        let response =
            reqwest::blocking::get(url).map_err(|e| TransportError::Request(e.to_string()))?;
        let status = response.status().as_u16();
        let body = response
            .bytes()
            .map_err(|e| TransportError::Request(e.to_string()))?
            .to_vec();

        Ok(TransportResponse { status, body })
    }
}
