import ComposableArchitecture
import Foundation
import VotingModels

// MARK: - API Configuration

/// Mutable runtime configuration for the Shielded-Vote chain REST API and helper server.
/// URLs are resolved from the CDN service config at startup.
actor SvAPIConfigStore {
    static let shared = SvAPIConfigStore()

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

private enum SvAPIError: LocalizedError {
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
    let resolvedDefault = await SvAPIConfigStore.shared.baseURL
    let base = baseURL ?? resolvedDefault
    guard let url = URL(string: "\(base)\(path)") else {
        throw SvAPIError.invalidResponse("invalid URL: \(base)\(path)")
    }
    let (data, response) = try await httpSession.data(from: url)
    guard let http = response as? HTTPURLResponse else {
        throw SvAPIError.invalidResponse("not an HTTP response")
    }
    guard http.statusCode == 200 else {
        let body = String(data: data, encoding: .utf8) ?? ""
        throw SvAPIError.httpError(statusCode: http.statusCode, message: body)
    }
    guard let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        throw SvAPIError.invalidResponse("expected JSON object")
    }
    return json
}

private func postJSON(_ path: String, body: [String: Any], baseURL: String? = nil) async throws -> [String: Any] {
    let resolvedDefault = await SvAPIConfigStore.shared.baseURL
    let base = baseURL ?? resolvedDefault
    guard let url = URL(string: "\(base)\(path)") else {
        throw SvAPIError.invalidResponse("invalid URL: \(base)\(path)")
    }
    var request = URLRequest(url: url)
    request.httpMethod = "POST"
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.httpBody = try JSONSerialization.data(withJSONObject: body)

    let (data, response) = try await httpSession.data(for: request)
    guard let http = response as? HTTPURLResponse else {
        throw SvAPIError.invalidResponse("not an HTTP response")
    }
    guard http.statusCode == 200 else {
        // 422 = chain processed the request but rejected the TX (non-zero CheckTx code).
        // Parse the structured body for code/log instead of returning a raw HTTP error.
        if http.statusCode == 422,
           let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
            let code = (json["code"] as? NSNumber)?.uint32Value ?? 0
            let log = json["log"] as? String ?? ""
            if code != 0 {
                throw SvAPIError.txFailed(code: code, log: log)
            }
        }
        let body = String(data: data, encoding: .utf8) ?? ""
        throw SvAPIError.httpError(statusCode: http.statusCode, message: body)
    }
    guard let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        throw SvAPIError.invalidResponse("expected JSON object")
    }
    return json
}

/// POST JSON to a specific vote server URL. Returns parsed JSON response.
private func postServerJSON(_ serverURL: String, _ path: String, body: [String: Any]) async throws -> [String: Any] {
    guard let url = URL(string: "\(serverURL)\(path)") else {
        throw SvAPIError.invalidResponse("invalid URL: \(serverURL)\(path)")
    }
    var request = URLRequest(url: url)
    request.httpMethod = "POST"
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.httpBody = try JSONSerialization.data(withJSONObject: body)

    let (data, response) = try await fastHttpSession.data(for: request)
    guard let http = response as? HTTPURLResponse else {
        throw SvAPIError.invalidResponse("not an HTTP response")
    }
    guard http.statusCode == 200 else {
        let body = String(data: data, encoding: .utf8) ?? ""
        throw SvAPIError.httpError(statusCode: http.statusCode, message: body)
    }
    guard let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        throw SvAPIError.invalidResponse("expected JSON object")
    }
    return json
}

/// Parse a broadcast TX response into TxResult. Throws on non-zero code.
private func parseTxResult(_ json: [String: Any]) throws -> TxResult {
    let txHash = json["tx_hash"] as? String ?? ""
    let code = (json["code"] as? NSNumber)?.uint32Value ?? 0
    let log = json["log"] as? String ?? ""
    if code != 0 {
        throw SvAPIError.txFailed(code: code, log: log)
    }
    return TxResult(txHash: txHash, code: code, log: log)
}

// MARK: - Broadcast Retry

/// Whether a broadcast error is transient and worth retrying.
/// Network failures and 502/503 (CometBFT gateway errors) are retryable.
/// Deterministic failures like 422 (CheckTx rejection) and 400 (bad request) are not.
private func isBroadcastRetryable(_ error: Error) -> Bool {
    if error is URLError { return true }
    if case SvAPIError.httpError(let status, _) = error {
        return status == 502 || status == 503
    }
    return false
}

/// Retry an async operation with exponential backoff.
/// Only retries when `isRetryable` returns true for the thrown error.
private func retryWithBackoff<T>(
    maxAttempts: Int = 3,
    initialDelay: TimeInterval = 2,
    factor: Double = 2,
    isRetryable: (Error) -> Bool,
    operation: () async throws -> T
) async throws -> T {
    var delay = initialDelay
    for attempt in 1...maxAttempts {
        do {
            return try await operation()
        } catch {
            let isLast = attempt == maxAttempts
            if isLast || !isRetryable(error) { throw error }
            print("[shielded-vote-api] broadcast attempt \(attempt)/\(maxAttempts) failed (\(error.localizedDescription)), retrying in \(delay)s")
            try await Task.sleep(for: .seconds(delay))
            delay *= factor
        }
    }
    fatalError("unreachable")
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

/// Parse a VotingSession from the "round" JSON object returned by GET /shielded-vote/v1/round/{id}.
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
                await SvAPIConfigStore.shared.configure(from: config)
                await ServerHealthTracker.shared.initialize(
                    serverURLs: config.voteServers.map(\.url)
                )
                let base = await SvAPIConfigStore.shared.baseURL
                let pir = await SvAPIConfigStore.shared.pirServerURL
                print("[VotingAPI] URLs configured: base=\(base), servers=\(config.voteServers.count), pir=\(pir)")
            },
            fetchActiveVotingSession: {
                let json = try await getJSON("/shielded-vote/v1/rounds/active")
                guard let round = json["round"] as? [String: Any] else {
                    throw SvAPIError.invalidResponse("missing 'round' in response")
                }
                return try parseVotingSession(from: round)
            },
            fetchAllRounds: {
                let json = try await getJSON("/shielded-vote/v1/rounds")
                guard let roundsArray = json["rounds"] as? [[String: Any]] else {
                    // No rounds — return empty
                    return []
                }
                return try roundsArray.map { try parseVotingSession(from: $0) }
            },
            fetchRoundById: { roundIdHex in
                let json = try await getJSON("/shielded-vote/v1/round/\(roundIdHex)")
                guard let round = json["round"] as? [String: Any] else {
                    throw SvAPIError.invalidResponse("missing 'round' in response")
                }
                return try parseVotingSession(from: round)
            },
            fetchTallyResults: { roundIdHex in
                let json = try await getJSON("/shielded-vote/v1/tally-results/\(roundIdHex)")
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
                let json = try await getJSON("/shielded-vote/v1/commitment-tree/\(height)")
                guard let tree = json["tree"] as? [String: Any] else {
                    throw SvAPIError.invalidResponse("missing 'tree' in response")
                }
                return parseCommitmentTree(from: tree)
            },
            fetchLatestCommitmentTree: {
                let json = try await getJSON("/shielded-vote/v1/commitment-tree/latest")
                guard let tree = json["tree"] as? [String: Any] else {
                    throw SvAPIError.invalidResponse("missing 'tree' in response")
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
                    "van_cmx": registration.vanCmx.base64EncodedString(),
                    "gov_nullifiers": registration.govNullifiers.map { $0.base64EncodedString() },
                    "proof": registration.proof.base64EncodedString(),
                    "vote_round_id": registration.voteRoundId.base64EncodedString()
                ]
                return try await retryWithBackoff(isRetryable: isBroadcastRetryable) {
                    let json = try await postJSON("/shielded-vote/v1/delegate-vote", body: body)
                    return try parseTxResult(json)
                }
            },
            submitVoteCommitment: { bundle, signature in
                // voteRoundId is a hex string; chain expects base64-encoded bytes
                let roundIdBytes = dataFromHex(bundle.voteRoundId)
                let body: [String: Any] = [
                    "van_nullifier": bundle.vanNullifier.base64EncodedString(),
                    "vote_authority_note_new": bundle.voteAuthorityNoteNew.base64EncodedString(),
                    "vote_commitment": bundle.voteCommitment.base64EncodedString(),
                    "proposal_id": bundle.proposalId,
                    "proof": bundle.proof.base64EncodedString(),
                    "vote_round_id": roundIdBytes.base64EncodedString(),
                    "vote_comm_tree_anchor_height": bundle.anchorHeight,
                    "r_vpk": bundle.rVpkBytes.base64EncodedString(),
                    "vote_auth_sig": signature.voteAuthSig.base64EncodedString()
                ]
                return try await retryWithBackoff(isRetryable: isBroadcastRetryable) {
                    let json = try await postJSON("/shielded-vote/v1/cast-vote", body: body)
                    return try parseTxResult(json)
                }
            },
            delegateShares: { payloads, roundIdHex in
                // Send each share to ceil(s/2) healthy helpers, balancing
                // censorship resistance (redundancy) against amount privacy
                // (limiting servers that see each share's ciphertext).
                // The chain deduplicates via share nullifiers — only the first
                // MsgRevealShare per nullifier is accepted.
                let tracker = ServerHealthTracker.shared
                let healthy = await tracker.healthyServers()
                let quorum = max(1, (healthy.count + 1) / 2)

                var lastError: Error?
                for (i, payload) in payloads.enumerated() {
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

                    // Pick `quorum` distinct servers uniformly at random for each
                    // share independently, so no single server sees a correlated
                    // subset of the voter's shares.
                    let targets = Array(healthy.shuffled().prefix(quorum))

                    // Send to all targets concurrently; the share is "delegated" if
                    // at least one server accepted it.
                    var accepted = false
                    var shareError: Error?
                    await withTaskGroup(of: (String, Bool).self) { group in
                        for server in targets {
                            group.addTask {
                                do {
                                    _ = try await postServerJSON(server, "/api/v1/shares", body: body)
                                    await tracker.recordSuccess(for: server)
                                    return (server, true)
                                } catch {
                                    await tracker.recordFailure(for: server)
                                    return (server, false)
                                }
                            }
                        }
                        for await (server, ok) in group {
                            if ok {
                                accepted = true
                            } else {
                                print("[VotingAPI] Share \(i) failed on \(server)")
                            }
                        }
                    }

                    if !accepted {
                        // All quorum servers failed — try remaining healthy servers as fallback.
                        let targetSet = Set(targets)
                        let fallbacks = await tracker.healthyServers().filter { !targetSet.contains($0) }.shuffled()
                        for fallback in fallbacks {
                            do {
                                _ = try await postServerJSON(fallback, "/api/v1/shares", body: body)
                                await tracker.recordSuccess(for: fallback)
                                accepted = true
                                print("[VotingAPI] Share \(i) succeeded on fallback \(fallback)")
                                break
                            } catch {
                                await tracker.recordFailure(for: fallback)
                                shareError = error
                            }
                        }
                    }

                    if !accepted {
                        print("[VotingAPI] Share \(i) failed on all servers")
                        lastError = shareError ?? SvAPIError.invalidResponse("all servers rejected share \(i)")
                    }
                }

                if let lastError {
                    throw lastError
                }
            },
            fetchProposalTally: { roundId, proposalId in
                let roundIdHex = roundId.map { String(format: "%02x", $0) }.joined()
                let json = try await getJSON("/shielded-vote/v1/tally-results/\(roundIdHex)")
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
                    let json = try await getJSON("/shielded-vote/v1/commitment-tree/latest")
                    guard let tree = json["tree"] as? [String: Any] else {
                        throw SvAPIError.invalidResponse("missing 'tree' in response")
                    }
                    let state = parseCommitmentTree(from: tree)
                    if state.nextIndex > previousNextIndex {
                        return state
                    }
                    try await Task.sleep(for: .seconds(1))
                }
                throw SvAPIError.commitmentTreeTimeout(seconds: timeoutSeconds)
            },
            checkTxConfirmed: { txHash in
                do {
                    let base = await SvAPIConfigStore.shared.baseURL
                    guard let url = URL(string: "\(base)/shielded-vote/v1/tx/\(txHash)") else { return nil }
                    let (data, response) = try await httpSession.data(from: url)
                    guard let http = response as? HTTPURLResponse else { return nil }

                    // 200 = TX confirmed with code 0; 422 = TX confirmed with non-zero code.
                    // Both mean the TX is in a block — parse the body for height/code/log.
                    guard http.statusCode == 200 || http.statusCode == 422 else { return nil }

                    guard let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
                    let height = parseUInt64(json["height"])
                    let code = parseUInt32(json["code"])
                    let log = json["log"] as? String ?? ""
                    return TxConfirmation(height: height, code: code, log: log)
                } catch {
                    // TX not yet confirmed (404 or network error)
                    return nil
                }
            }
        )
    }
}
