import Combine
import ComposableArchitecture
import Foundation
import VotingModels

extension DependencyValues {
    public var votingCrypto: VotingCryptoClient {
        get { self[VotingCryptoClient.self] }
        set { self[VotingCryptoClient.self] = newValue }
    }
}

@DependencyClient
public struct VotingCryptoClient {
    // --- State stream (DB → UI, follows SDKSynchronizer pattern) ---
    public var stateStream: @Sendable () -> AnyPublisher<VotingDbState, Never>
        = { Empty().eraseToAnyPublisher() }

    /// Re-publish the current DB state for the given round, triggering stateStream subscribers.
    public var refreshState: @Sendable (_ roundId: String) async -> Void = { _ in }

    // --- Database lifecycle ---
    public var openDatabase: @Sendable (_ path: String) async throws -> Void
    public var initRound: @Sendable (_ params: VotingRoundParams, _ sessionJson: String?) async throws -> Void
    public var getRoundState: @Sendable (_ roundId: String) async throws -> RoundStateInfo
    public var getVotes: @Sendable (_ roundId: String) async throws -> [VoteRecord]
    public var listRounds: @Sendable () async throws -> [RoundSummaryInfo]
    public var clearRound: @Sendable (_ roundId: String) async throws -> Void

    // --- Wallet notes ---
    public var getWalletNotes: @Sendable (
        _ walletDbPath: String,
        _ snapshotHeight: UInt64,
        _ networkId: UInt32
    ) async throws -> [NoteInfo]

    // --- Bundle management ---
    public var setupBundles: @Sendable (_ roundId: String, _ notes: [NoteInfo]) async throws -> BundleSetupResult
    public var getBundleCount: @Sendable (_ roundId: String) async throws -> UInt32

    // --- Witness generation & verification ---
    public var generateNoteWitnesses: @Sendable (
        _ roundId: String,
        _ bundleIndex: UInt32,
        _ walletDbPath: String,
        _ notes: [NoteInfo]
    ) async throws -> [WitnessData]
    public var verifyWitness: @Sendable (_ witness: WitnessData) async throws -> Bool

    // --- Crypto operations ---
    public var generateHotkey: @Sendable (_ roundId: String, _ seed: [UInt8]) async throws -> VotingHotkey
    /// High-level boundary for sign-action generation:
    /// derives delegation inputs from seeds and constructs a valid delegation action.
    public var buildDelegationSignAction: @Sendable (
        _ roundId: String,
        _ bundleIndex: UInt32,
        _ notes: [NoteInfo],
        _ senderSeed: [UInt8],
        _ hotkeySeed: [UInt8],
        _ networkId: UInt32,
        _ accountIndex: UInt32
    ) async throws -> DelegationAction
    /// Build a governance-specific PCZT for Keystone signing.
    /// The PCZT's single Orchard action IS the governance dummy action, so Keystone's
    /// SpendAuth signature will be over the governance-bound ZIP-244 sighash.
    public var buildGovernancePczt: @Sendable (
        _ roundId: String,
        _ bundleIndex: UInt32,
        _ notes: [NoteInfo],
        _ senderSeed: [UInt8],
        _ hotkeySeed: [UInt8],
        _ networkId: UInt32,
        _ accountIndex: UInt32,
        _ roundName: String
    ) async throws -> GovernancePcztResult
    public var storeTreeState: @Sendable (_ roundId: String, _ treeState: Data) async throws -> Void
    public var extractSpendAuthSignatureFromSignedPczt: @Sendable (
        _ signedPczt: Data,
        _ actionIndex: UInt32
    ) throws -> Data
    /// Build and prove the real delegation ZKP (#1). Long-running.
    /// Loads data from voting DB and wallet DB, fetches IMT proofs from server,
    /// generates a real Halo2 proof, and reports progress.
    public var buildAndProveDelegation: @Sendable (
        _ roundId: String,
        _ bundleIndex: UInt32,
        _ bundleNotes: [NoteInfo],
        _ walletDbPath: String,
        _ senderSeed: [UInt8],
        _ hotkeySeed: [UInt8],
        _ networkId: UInt32,
        _ accountIndex: UInt32,
        _ imtServerUrl: String
    ) -> AsyncThrowingStream<ProofEvent, Error>
        = { _, _, _, _, _, _, _, _, _ in AsyncThrowingStream { $0.finish() } }
    public var decomposeWeight: @Sendable (_ weight: UInt64) -> [UInt64] = { _ in [] }
    public var encryptShares: @Sendable (
        _ roundId: String,
        _ shares: [UInt64]
    ) async throws -> [EncryptedShare]
    public var buildVoteCommitment: @Sendable (
        _ roundId: String,
        _ bundleIndex: UInt32,
        _ hotkeySeed: [UInt8],
        _ networkId: UInt32,
        _ proposalId: UInt32,
        _ choice: VoteChoice,
        _ numOptions: UInt32,
        _ vanAuthPath: [Data],
        _ vanPosition: UInt32,
        _ anchorHeight: UInt32
    ) -> AsyncThrowingStream<VoteCommitmentBuildEvent, Error>
        = { _, _, _, _, _, _, _, _, _, _ in AsyncThrowingStream { $0.finish() } }
    public var buildSharePayloads: @Sendable (
        _ encShares: [EncryptedShare],
        _ commitment: VoteCommitmentBundle,
        _ voteDecision: VoteChoice,
        _ numOptions: UInt32,
        _ vcTreePosition: UInt64
    ) async throws -> [SharePayload]
    /// Reconstruct the full chain-ready delegation TX payload from DB + seed.
    /// Call after `buildAndProveDelegation` completes.
    public var getDelegationSubmission: @Sendable (
        _ roundId: String,
        _ bundleIndex: UInt32,
        _ senderSeed: [UInt8],
        _ networkId: UInt32,
        _ accountIndex: UInt32
    ) async throws -> DelegationRegistration
    public var storeVanPosition: @Sendable (_ roundId: String, _ bundleIndex: UInt32, _ position: UInt32) async throws -> Void
    public var syncVoteTree: @Sendable (_ roundId: String, _ nodeUrl: String) async throws -> UInt32
    public var generateVanWitness: @Sendable (_ roundId: String, _ bundleIndex: UInt32, _ anchorHeight: UInt32) async throws -> VanWitness
    public var markVoteSubmitted: @Sendable (_ roundId: String, _ bundleIndex: UInt32, _ proposalId: UInt32) async throws -> Void
    /// Compute canonical cast-vote sighash, decompress r_vpk, and sign.
    /// Call after `buildVoteCommitment` completes, before `submitVoteCommitment`.
    public var signCastVote: @Sendable (
        _ hotkeySeed: [UInt8],
        _ networkId: UInt32,
        _ bundle: VoteCommitmentBundle
    ) async throws -> CastVoteSignature
    /// Extract the Orchard nc_root from a protobuf-encoded TreeState.
    public var extractNcRoot: @Sendable (_ treeStateBytes: Data) throws -> Data
}
