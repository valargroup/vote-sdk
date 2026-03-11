import ComposableArchitecture
import Foundation
import VotingModels

extension DependencyValues {
    public var votingAPI: VotingAPIClient {
        get { self[VotingAPIClient.self] }
        set { self[VotingAPIClient.self] = newValue }
    }
}

@DependencyClient
public struct VotingAPIClient {
    /// Fetch service config: tries local override first, then CDN, then falls back to defaults.
    public var fetchServiceConfig: @Sendable () async throws -> VotingServiceConfig
    /// Configure the API client to use URLs from the resolved service config.
    public var configureURLs: @Sendable (_ config: VotingServiceConfig) async -> Void
    public var fetchActiveVotingSession: @Sendable () async throws -> VotingSession
    public var fetchAllRounds: @Sendable () async throws -> [VotingSession]
    public var fetchRoundById: @Sendable (_ roundIdHex: String) async throws -> VotingSession
    public var fetchTallyResults: @Sendable (_ roundIdHex: String) async throws -> [UInt32: TallyResult]
    public var fetchVotingWeight: @Sendable (_ snapshotHeight: UInt64) async throws -> UInt64
    public var fetchNoteInclusionProofs: @Sendable (_ commitments: [Data]) async throws -> [Data]
    public var fetchNullifierExclusionProofs: @Sendable (_ nullifiers: [Data]) async throws -> [Data]
    public var fetchCommitmentTreeState: @Sendable (_ height: UInt64) async throws -> CommitmentTreeState
    public var fetchLatestCommitmentTree: @Sendable () async throws -> CommitmentTreeState
    public var submitDelegation: @Sendable (_ registration: DelegationRegistration) async throws -> TxResult
    public var submitVoteCommitment: @Sendable (_ bundle: VoteCommitmentBundle, _ signature: CastVoteSignature) async throws -> TxResult
    /// Distribute shares across available vote servers. Config must be set via `configureURLs` first.
    public var delegateShares: @Sendable (_ payloads: [SharePayload], _ roundIdHex: String) async throws -> Void
    public var fetchProposalTally: @Sendable (_ roundId: Data, _ proposalId: UInt32) async throws -> TallyResult
    public var awaitCommitmentTreeGrowth: @Sendable (_ previousNextIndex: UInt64, _ timeoutSeconds: TimeInterval) async throws -> CommitmentTreeState
    /// Check whether a TX has been included in a block. Returns nil if not yet confirmed (404 or network error).
    public var checkTxConfirmed: @Sendable (_ txHash: String) async throws -> TxConfirmation?
}
