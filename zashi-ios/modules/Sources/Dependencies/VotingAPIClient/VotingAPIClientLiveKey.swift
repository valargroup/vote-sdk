import ComposableArchitecture
import Foundation
import VotingModels

// MARK: - API Configuration

/// Mutable runtime configuration for the Zally chain REST API and helper server.
/// URLs are resolved from the CDN service config at startup.
actor ZallyAPIConfigStore {
    static let shared = ZallyAPIConfigStore()

    /// Primary vote server URL (serves both chain API and helper endpoints).
    var baseURL = "https://46-101-255-48.sslip.io"
    /// All vote server URLs from CDN config (used for share distribution).
    var voteServerURLs: [String] = ["https://46-101-255-48.sslip.io"]
    /// Primary PIR server URL.
    var pirServerURL = "https://46-101-255-48.sslip.io/nullifier"

    func configure(from config: VotingServiceConfig) {
        if let first = config.voteServers.first {
            baseURL = first.url
        }
        voteServerURLs = config.voteServers.map(\.url)
        if let first = config.pirServers.first {
            pirServerURL = first.url
        }
    }
}

// MARK: - Errors

private enum ZallyAPIError: LocalizedError {
    case httpError(statusCode: Int, message: String)
    case invalidResponse(String)
    case txFailed(code: UInt32, log: String)
    case commitmentTreeTimeout(seconds: TimeInterval)

    var errorDescription: String? {
        switch self {
        case .httpError(let code, let message):
            return "HTTP \(code): \(message)"
        case .invalidResponse(let detail):
            return "Invalid API response: \(detail)"
        case .txFailed(let code, let log):
            return "Transaction failed (code \(code)): \(log)"
        case .commitmentTreeTimeout(let seconds):
            return "Commitment tree did not grow within \(Int(seconds))s — the TX may not have been included in a block yet."
        }
    }
}

// MARK: - HTTP Helpers

/// URLSession configured with a long timeout to accommodate ZKP verification (30-60s).
private let httpSession: URLSession = {
    let config = URLSessionConfiguration.default
    config.timeoutIntervalForRequest = 120
    return URLSession(configuration: config)
}()

/// Fast URLSession for share POSTs and health probes (5s timeout).
/// Share delivery should fail fast so we can failover to another server.
private let fastHttpSession: URLSession = {
    let config = URLSessionConfiguration.default
    config.timeoutIntervalForRequest = 5
    return URLSession(configuration: config)
}()

private func getJSON(_ path: String, baseURL: String? = nil) async throws -> [String: Any] {
    let resolvedDefault = await ZallyAPIConfigStore.shared.baseURL
    let base = baseURL ?? resolvedDefault
    guard let url = URL(string: "\(base)\(path)") else {
        throw ZallyAPIError.invalidResponse("invalid URL: \(base)\(path)")
    }
    let (data, response) = try await httpSession.data(from: url)
    guard let http = response as? HTTPURLResponse else {
        throw ZallyAPIError.invalidResponse("not an HTTP response")
    }
    guard http.statusCode == 200 else {
        let body = String(data: data, encoding: .utf8) ?? ""
        throw ZallyAPIError.httpError(statusCode: http.statusCode, message: body)
    }
    guard let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        throw ZallyAPIError.invalidResponse("expected JSON object")
    }
    return json
}

private func postJSON(_ path: String, body: [String: Any], baseURL: String? = nil) async throws -> [String: Any] {
    let resolvedDefault = await ZallyAPIConfigStore.shared.baseURL
    let base = baseURL ?? resolvedDefault
    guard let url = URL(string: "\(base)\(path)") else {
        throw ZallyAPIError.invalidResponse("invalid URL: \(base)\(path)")
    }
    var request = URLRequest(url: url)
    request.httpMethod = "POST"
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.httpBody = try JSONSerialization.data(withJSONObject: body)

    let (data, response) = try await httpSession.data(for: request)
    guard let http = response as? HTTPURLResponse else {
        throw ZallyAPIError.invalidResponse("not an HTTP response")
    }
    guard http.statusCode == 200 else {
        let body = String(data: data, encoding: .utf8) ?? ""
        throw ZallyAPIError.httpError(statusCode: http.statusCode, message: body)
    }
    guard let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        throw ZallyAPIError.invalidResponse("expected JSON object")
    }
    return json
}

/// POST JSON to a specific vote server URL. Returns parsed JSON response.
private func postServerJSON(_ serverURL: String, _ path: String, body: [String: Any]) async throws -> [String: Any] {
    guard let url = URL(string: "\(serverURL)\(path)") else {
        throw ZallyAPIError.invalidResponse("invalid URL: \(serverURL)\(path)")
    }
    var request = URLRequest(url: url)
    request.httpMethod = "POST"
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.httpBody = try JSONSerialization.data(withJSONObject: body)

    let (data, response) = try await fastHttpSession.data(for: request)
    guard let http = response as? HTTPURLResponse else {
        throw ZallyAPIError.invalidResponse("not an HTTP response")
    }
    guard http.statusCode == 200 else {
        let body = String(data: data, encoding: .utf8) ?? ""
        throw ZallyAPIError.httpError(statusCode: http.statusCode, message: body)
    }
    guard let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        throw ZallyAPIError.invalidResponse("expected JSON object")
    }
    return json
}

/// Parse a broadcast TX response into TxResult. Throws on non-zero code.
private func parseTxResult(_ json: [String: Any]) throws -> TxResult {
    let txHash = json["tx_hash"] as? String ?? ""
    let code = (json["code"] as? NSNumber)?.uint32Value ?? 0
    let log = json["log"] as? String ?? ""
    if code != 0 {
        throw ZallyAPIError.txFailed(code: code, log: log)
    }
    return TxResult(txHash: txHash, code: code, log: log)
}

// MARK: - Protobuf JSON Parsing Helpers

/// Parse a uint64 value that may come as a string (protobuf JSON) or number.
private func parseUInt64(_ value: Any?) -> UInt64 {
    if let str = value as? String, let n = UInt64(str) { return n }
    if let num = value as? NSNumber { return num.uint64Value }
    return 0
}

/// Parse a uint32 value from JSON (number or string).
private func parseUInt32(_ value: Any?) -> UInt32 {
    if let str = value as? String, let n = UInt32(str) { return n }
    if let num = value as? NSNumber { return num.uint32Value }
    return 0
}

/// Decode base64-encoded bytes, returning empty Data on failure.
private func parseBase64(_ value: Any?) -> Data {
    guard let str = value as? String, let data = Data(base64Encoded: str) else { return Data() }
    return data
}

/// Convert hex string to Data.
private func dataFromHex(_ hex: String) -> Data {
    var data = Data()
    var idx = hex.startIndex
    while idx < hex.endIndex {
        let next = hex.index(idx, offsetBy: 2, limitedBy: hex.endIndex) ?? hex.endIndex
        if let byte = UInt8(hex[idx..<next], radix: 16) {
            data.append(byte)
        }
        idx = next
    }
    return data
}

// MARK: - Response Parsers

/// Parse a VotingSession from the "round" JSON object returned by GET /zally/v1/round/{id}.
private func parseVotingSession(from round: [String: Any]) throws -> VotingSession {
    let voteEndTimeUnix = parseUInt64(round["vote_end_time"])
    let voteEndTime = Date(timeIntervalSince1970: TimeInterval(voteEndTimeUnix))
    let statusRaw = parseUInt32(round["status"])

    // Parse proposals array with options
    var proposals: [Proposal] = []
    if let proposalsJSON = round["proposals"] as? [[String: Any]] {
        proposals = proposalsJSON.map { p in
            var options: [VoteOption] = []
            if let optionsJSON = p["options"] as? [[String: Any]] {
                options = optionsJSON.map { o in
                    VoteOption(
                        index: parseUInt32(o["index"]),
                        label: o["label"] as? String ?? "Option \(parseUInt32(o["index"]))"
                    )
                }
            }
            return Proposal(
                id: parseUInt32(p["id"]),
                title: p["title"] as? String ?? "",
                description: p["description"] as? String ?? "",
                options: options
            )
        }
    }

    return VotingSession(
        voteRoundId: parseBase64(round["vote_round_id"]),
        snapshotHeight: parseUInt64(round["snapshot_height"]),
        snapshotBlockhash: parseBase64(round["snapshot_blockhash"]),
        proposalsHash: parseBase64(round["proposals_hash"]),
        voteEndTime: voteEndTime,
        eaPK: parseBase64(round["ea_pk"]),
        vkZkp1: parseBase64(round["vk_zkp1"]),
        vkZkp2: parseBase64(round["vk_zkp2"]),
        vkZkp3: parseBase64(round["vk_zkp3"]),
        ncRoot: parseBase64(round["nc_root"]),
        nullifierIMTRoot: parseBase64(round["nullifier_imt_root"]),
        creator: round["creator"] as? String ?? "",
        description: round["description"] as? String ?? "",
        proposals: proposals,
        status: SessionStatus(rawValue: statusRaw) ?? .unspecified,
        createdAtHeight: parseUInt64(round["created_at_height"]),
        title: round["title"] as? String ?? ""
    )
}

/// Parse a CommitmentTreeState from the "tree" JSON object.
private func parseCommitmentTree(from tree: [String: Any]) -> CommitmentTreeState {
    CommitmentTreeState(
        nextIndex: parseUInt64(tree["next_index"]),
        root: parseBase64(tree["root"]),
        height: parseUInt64(tree["height"])
    )
}

// MARK: - Live Implementation

extension VotingAPIClient: DependencyKey {
    public static var liveValue: Self {
        Self(
            fetchServiceConfig: {
                // 1. Check for local override in app bundle (debug builds only)
                #if DEBUG
                if let localURL = Bundle.main.url(
                    forResource: "voting-config-local",
                    withExtension: "json"
                ) {
                    if let data = try? Data(contentsOf: localURL),
                       let config = try? JSONDecoder().decode(VotingServiceConfig.self, from: data) {
                        print("[VotingAPI] Using local override config: \(config.voteServers.count) vote servers")
                        return config
                    }
                }
                #endif

                // 2. Try CDN
                do {
                    let (data, response) = try await httpSession.data(from: VotingServiceConfig.cdnURL)
                    if let http = response as? HTTPURLResponse, http.statusCode == 200 {
                        let config = try JSONDecoder().decode(VotingServiceConfig.self, from: data)
                        print("[VotingAPI] Loaded CDN config: \(config.voteServers.count) vote servers")
                        return config
                    }
                } catch {
                    print("[VotingAPI] CDN config fetch failed: \(error)")
                }

                // 3. Fall back to deployed dev server defaults
                print("[VotingAPI] Using fallback config (deployed dev server)")
                return .fallback
            },
            configureURLs: { config in
                await ZallyAPIConfigStore.shared.configure(from: config)
                await ServerHealthTracker.shared.initialize(
                    serverURLs: config.voteServers.map(\.url)
                )
                let base = await ZallyAPIConfigStore.shared.baseURL
                let pir = await ZallyAPIConfigStore.shared.pirServerURL
                print("[VotingAPI] URLs configured: base=\(base), servers=\(config.voteServers.count), pir=\(pir)")
            },
            fetchActiveVotingSession: {
                let json = try await getJSON("/zally/v1/rounds/active")
                guard let round = json["round"] as? [String: Any] else {
                    throw ZallyAPIError.invalidResponse("missing 'round' in response")
                }
                return try parseVotingSession(from: round)
            },
            fetchAllRounds: {
                let json = try await getJSON("/zally/v1/rounds")
                guard let roundsArray = json["rounds"] as? [[String: Any]] else {
                    // No rounds — return empty
                    return []
                }
                return try roundsArray.map { try parseVotingSession(from: $0) }
            },
            fetchRoundById: { roundIdHex in
                let json = try await getJSON("/zally/v1/round/\(roundIdHex)")
                guard let round = json["round"] as? [String: Any] else {
                    throw ZallyAPIError.invalidResponse("missing 'round' in response")
                }
                return try parseVotingSession(from: round)
            },
            fetchTallyResults: { roundIdHex in
                let json = try await getJSON("/zally/v1/tally-results/\(roundIdHex)")
                guard let results = json["results"] as? [[String: Any]] else {
                    return [:]
                }
                // Group by proposal_id
                var grouped: [UInt32: [TallyResult.Entry]] = [:]
                for entry in results {
                    let proposalId = parseUInt32(entry["proposal_id"])
                    let tallyEntry = TallyResult.Entry(
                        decision: parseUInt32(entry["vote_decision"]),
                        amount: parseUInt64(entry["total_value"])
                    )
                    grouped[proposalId, default: []].append(tallyEntry)
                }
                return grouped.mapValues { TallyResult(entries: $0) }
            },
            fetchVotingWeight: { _ in
                // Weight is computed locally from wallet notes; this endpoint is unused.
                fatalError("fetchVotingWeight is deprecated — weight is computed from wallet notes")
            },
            fetchNoteInclusionProofs: { _ in
                // Witnesses are generated by votingCrypto; this endpoint is unused.
                fatalError("fetchNoteInclusionProofs is deprecated — witnesses come from votingCrypto")
            },
            fetchNullifierExclusionProofs: { _ in
                // Nullifier exclusion proofs are fetched by the Rust PIR client; this endpoint is unused.
                fatalError("fetchNullifierExclusionProofs is deprecated — handled by PIR client")
            },
            fetchCommitmentTreeState: { height in
                let json = try await getJSON("/zally/v1/commitment-tree/\(height)")
                guard let tree = json["tree"] as? [String: Any] else {
                    throw ZallyAPIError.invalidResponse("missing 'tree' in response")
                }
                return parseCommitmentTree(from: tree)
            },
            fetchLatestCommitmentTree: {
                let json = try await getJSON("/zally/v1/commitment-tree/latest")
                guard let tree = json["tree"] as? [String: Any] else {
                    throw ZallyAPIError.invalidResponse("missing 'tree' in response")
                }
                return parseCommitmentTree(from: tree)
            },
            submitDelegation: { registration in
                let body: [String: Any] = [
                    "rk": registration.rk.base64EncodedString(),
                    "spend_auth_sig": registration.spendAuthSig.base64EncodedString(),
                    "sighash": registration.sighash.base64EncodedString(),
                    "signed_note_nullifier": registration.signedNoteNullifier.base64EncodedString(),
                    "cmx_new": registration.cmxNew.base64EncodedString(),
                    "enc_memo": registration.encMemo.base64EncodedString(),
                    "van_cmx": registration.vanCmx.base64EncodedString(),
                    "gov_nullifiers": registration.govNullifiers.map { $0.base64EncodedString() },
                    "proof": registration.proof.base64EncodedString(),
                    "vote_round_id": registration.voteRoundId.base64EncodedString()
                ]
                let json = try await postJSON("/zally/v1/delegate-vote", body: body)
                return try parseTxResult(json)
            },
            submitVoteCommitment: { bundle, signature in
                // voteRoundId is a hex string; chain expects base64-encoded bytes
                let roundIdBytes = dataFromHex(bundle.voteRoundId)
                let body: [String: Any] = [
                    "van_nullifier": bundle.vanNullifier.base64EncodedString(),
                    "r_vpk_x": signature.rVpkX.base64EncodedString(),
                    "r_vpk_y": signature.rVpkY.base64EncodedString(),
                    "vote_authority_note_new": bundle.voteAuthorityNoteNew.base64EncodedString(),
                    "vote_commitment": bundle.voteCommitment.base64EncodedString(),
                    "proposal_id": bundle.proposalId,
                    "proof": bundle.proof.base64EncodedString(),
                    "vote_round_id": roundIdBytes.base64EncodedString(),
                    "vote_comm_tree_anchor_height": bundle.anchorHeight,
                    "r_vpk": bundle.rVpkBytes.base64EncodedString(),
                    "sighash": signature.sighash.base64EncodedString(),
                    "vote_auth_sig": signature.voteAuthSig.base64EncodedString()
                ]
                let json = try await postJSON("/zally/v1/cast-vote", body: body)
                return try parseTxResult(json)
            },
            delegateShares: { payloads, roundIdHex in
                // Distribute shares across healthy vote servers with per-share failover.
                let tracker = ServerHealthTracker.shared
                let healthy = await tracker.healthyServers().shuffled()

                var lastError: Error?
                for (i, payload) in payloads.enumerated() {
                    let serverURL = healthy[i % healthy.count]

                    let body: [String: Any] = [
                        "shares_hash": payload.sharesHash.base64EncodedString(),
                        "proposal_id": payload.proposalId,
                        "vote_decision": payload.voteDecision,
                        "enc_share": [
                            "c1": payload.encShare.c1.base64EncodedString(),
                            "c2": payload.encShare.c2.base64EncodedString(),
                            "share_index": payload.encShare.shareIndex
                        ],
                        "share_index": payload.encShare.shareIndex,
                        "tree_position": payload.treePosition,
                        "vote_round_id": roundIdHex,
                        "share_comms": payload.shareComms.map { $0.base64EncodedString() },
                        "primary_blind": payload.primaryBlind.base64EncodedString()
                    ]

                    do {
                        _ = try await postServerJSON(serverURL, "/api/v1/shares", body: body)
                        await tracker.recordSuccess(for: serverURL)
                    } catch {
                        await tracker.recordFailure(for: serverURL)
                        print("[VotingAPI] Share \(i) failed on \(serverURL): \(error.localizedDescription)")

                        // Retry once on a different server
                        let fallbacks = await tracker.healthyServers().shuffled().filter { $0 != serverURL }
                        if let fallback = fallbacks.first {
                            do {
                                _ = try await postServerJSON(fallback, "/api/v1/shares", body: body)
                                await tracker.recordSuccess(for: fallback)
                                print("[VotingAPI] Share \(i) succeeded on fallback \(fallback)")
                                continue
                            } catch {
                                await tracker.recordFailure(for: fallback)
                                lastError = error
                                print("[VotingAPI] Share \(i) fallback also failed on \(fallback): \(error.localizedDescription)")
                            }
                        } else {
                            lastError = error
                        }
                    }
                }

                // If any shares failed after retry, surface the error so the outer retry loop can handle it
                if let lastError {
                    throw lastError
                }
            },
            fetchProposalTally: { roundId, proposalId in
                let roundIdHex = roundId.map { String(format: "%02x", $0) }.joined()
                let json = try await getJSON("/zally/v1/tally-results/\(roundIdHex)")
                guard let results = json["results"] as? [[String: Any]] else {
                    // No results yet — return empty tally
                    return TallyResult(entries: [])
                }
                let entries = results
                    .filter { parseUInt32($0["proposal_id"]) == proposalId }
                    .map { entry in
                        TallyResult.Entry(
                            decision: parseUInt32(entry["vote_decision"]),
                            amount: parseUInt64(entry["total_value"])
                        )
                    }
                return TallyResult(entries: entries)
            },
            awaitCommitmentTreeGrowth: { previousNextIndex, timeoutSeconds in
                let deadline = Date().addingTimeInterval(timeoutSeconds)
                while Date() < deadline {
                    let json = try await getJSON("/zally/v1/commitment-tree/latest")
                    guard let tree = json["tree"] as? [String: Any] else {
                        throw ZallyAPIError.invalidResponse("missing 'tree' in response")
                    }
                    let state = parseCommitmentTree(from: tree)
                    if state.nextIndex > previousNextIndex {
                        return state
                    }
                    try await Task.sleep(for: .seconds(1))
                }
                throw ZallyAPIError.commitmentTreeTimeout(seconds: timeoutSeconds)
            },
            checkTxConfirmed: { txHash in
                do {
                    let json = try await getJSON("/zally/v1/tx/\(txHash)")
                    let height = parseUInt64(json["height"])
                    let code = parseUInt32(json["code"])
                    return TxConfirmation(height: height, code: code)
                } catch {
                    // TX not yet confirmed (404 or network error)
                    return nil
                }
            }
        )
    }
}
