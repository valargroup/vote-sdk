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
    /// Delete bundle rows with index >= keepCount, removing skipped bundles
    /// so that proof_generated only considers signed+proven bundles.
    public var deleteSkippedBundles: @Sendable (_ roundId: String, _ keepCount: UInt32) async throws -> Void

    // --- Wallet notes ---
    public var getWalletNotes: @Sendable (
        _ walletDbPath: String,
        _ snapshotHeight: UInt64,
        _ networkId: UInt32,
        _ seedFingerprint: [UInt8]?,
        _ accountIndex: UInt32?
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
        _ roundName: String,
        _ orchardFvkOverride: Data?,
        _ keystoneSeedFingerprintOverride: Data?
    ) async throws -> GovernancePcztResult
    public var storeTreeState: @Sendable (_ roundId: String, _ treeState: Data) async throws -> Void
    public var extractSpendAuthSignatureFromSignedPczt: @Sendable (
        _ signedPczt: Data,
        _ actionIndex: UInt32
    ) throws -> Data
    /// Extract the ZIP-244 shielded sighash from finalized PCZT bytes.
    /// Returns the 32-byte sighash that Keystone signed internally.
    public var extractPcztSighash: @Sendable (_ pcztBytes: Data) throws -> Data
    /// Build and prove the real delegation ZKP (#1). Long-running.
    /// Loads data from voting DB and wallet DB, fetches IMT proofs from server,
    /// generates a real Halo2 proof, and reports progress.
    /// Requires `buildGovernancePczt` to have been called first for this bundle —
    /// it stores the delegation data (alpha, secrets, sighash) needed by the prover.
    public var buildAndProveDelegation: @Sendable (
        _ roundId: String,
        _ bundleIndex: UInt32,
        _ bundleNotes: [NoteInfo],
        _ senderSeed: [UInt8],
        _ hotkeySeed: [UInt8],
        _ networkId: UInt32,
        _ accountIndex: UInt32,
        _ pirServerUrl: String
    ) -> AsyncThrowingStream<ProofEvent, Error>
        = { _, _, _, _, _, _, _, _ in AsyncThrowingStream { $0.finish() } }
    /// Extract Orchard FVK bytes from a UFVK string.
    public var extractOrchardFvkFromUfvk: @Sendable (_ ufvkStr: String, _ networkId: UInt32) throws -> Data
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
    /// Reconstruct the delegation TX payload using a Keystone-provided signature.
    /// Uses the externally-provided signature and ZIP-244 sighash instead of
    /// deriving `ask` from seed.
    public var getDelegationSubmissionWithKeystoneSig: @Sendable (
        _ roundId: String,
        _ bundleIndex: UInt32,
        _ keystoneSig: Data,
        _ keystoneSighash: Data
    ) async throws -> DelegationRegistration
    public var storeVanPosition: @Sendable (_ roundId: String, _ bundleIndex: UInt32, _ position: UInt32) async throws -> Void
    public var syncVoteTree: @Sendable (_ roundId: String, _ nodeUrl: String) async throws -> UInt32
    public var generateVanWitness: @Sendable (_ roundId: String, _ bundleIndex: UInt32, _ anchorHeight: UInt32) async throws -> VanWitness
    public var markVoteSubmitted: @Sendable (_ roundId: String, _ bundleIndex: UInt32, _ proposalId: UInt32) async throws -> Void
    /// Drop the in-memory TreeClient so the next `syncVoteTree` starts fresh.
    /// Recovers from stale state after commitment tree timeout.
    public var resetTreeClient: @Sendable () async throws -> Void
    /// Decompress r_vpk and sign the canonical cast-vote sighash.
    /// Call after `buildVoteCommitment` completes, before `submitVoteCommitment`.
    public var signCastVote: @Sendable (
        _ hotkeySeed: [UInt8],
        _ networkId: UInt32,
        _ bundle: VoteCommitmentBundle
    ) async throws -> CastVoteSignature
    /// Extract the Orchard nc_root from a protobuf-encoded TreeState.
    public var extractNcRoot: @Sendable (_ treeStateBytes: Data) throws -> Data
}
