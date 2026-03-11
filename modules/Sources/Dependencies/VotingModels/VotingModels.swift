import Foundation

// MARK: - Session & Round

/// Full on-chain representation from VoteRound proto (zvote/v1/types.proto).
/// vote_round_id is a canonical 32-byte Pallas Fp value derived on-chain from
/// session setup fields via Poseidon hash.
public struct VotingSession: Equatable, Sendable {
    public let voteRoundId: Data
    public let snapshotHeight: UInt64
    public let snapshotBlockhash: Data
    public let proposalsHash: Data
    public let voteEndTime: Date
    public let eaPK: Data
    public let vkZkp1: Data
    public let vkZkp2: Data
    public let vkZkp3: Data
    public let ncRoot: Data
    public let nullifierIMTRoot: Data
    public let creator: String
    public let description: String
    public let proposals: [Proposal]
    public let status: SessionStatus
    public let createdAtHeight: UInt64
    public let title: String

    public init(
        voteRoundId: Data,
        snapshotHeight: UInt64,
        snapshotBlockhash: Data,
        proposalsHash: Data,
        voteEndTime: Date,
        eaPK: Data,
        vkZkp1: Data,
        vkZkp2: Data,
        vkZkp3: Data,
        ncRoot: Data,
        nullifierIMTRoot: Data,
        creator: String,
        description: String = "",
        proposals: [Proposal],
        status: SessionStatus,
        createdAtHeight: UInt64 = 0,
        title: String = ""
    ) {
        self.voteRoundId = voteRoundId
        self.snapshotHeight = snapshotHeight
        self.snapshotBlockhash = snapshotBlockhash
        self.proposalsHash = proposalsHash
        self.voteEndTime = voteEndTime
        self.eaPK = eaPK
        self.vkZkp1 = vkZkp1
        self.vkZkp2 = vkZkp2
        self.vkZkp3 = vkZkp3
        self.ncRoot = ncRoot
        self.nullifierIMTRoot = nullifierIMTRoot
        self.creator = creator
        self.description = description
        self.proposals = proposals
        self.status = status
        self.createdAtHeight = createdAtHeight
        self.title = title
    }
}

/// Maps to proto SessionStatus (zvote/v1/types.proto).
public enum SessionStatus: UInt32, Equatable, Sendable {
    case unspecified = 0
    case active = 1
    case tallying = 2
    case finalized = 3
}

/// Lightweight subset of VotingSession passed to crypto operations.
public struct VotingRoundParams: Equatable, Sendable {
    public let voteRoundId: Data
    public let snapshotHeight: UInt64
    public let eaPK: Data
    public let ncRoot: Data
    public let nullifierIMTRoot: Data

    public init(
        voteRoundId: Data,
        snapshotHeight: UInt64,
        eaPK: Data,
        ncRoot: Data,
        nullifierIMTRoot: Data
    ) {
        self.voteRoundId = voteRoundId
        self.snapshotHeight = snapshotHeight
        self.eaPK = eaPK
        self.ncRoot = ncRoot
        self.nullifierIMTRoot = nullifierIMTRoot
    }
}

// MARK: - Round State (from Rust storage)

public enum RoundPhaseInfo: Equatable, Sendable {
    case initialized
    case hotkeyGenerated
    case delegationConstructed
    case delegationProved
    case voteReady
}

public struct RoundStateInfo: Equatable, Sendable {
    public let roundId: String
    public let phase: RoundPhaseInfo
    public let snapshotHeight: UInt64
    public let hotkeyAddress: String?
    public let delegatedWeight: UInt64?
    public let proofGenerated: Bool

    public init(
        roundId: String,
        phase: RoundPhaseInfo,
        snapshotHeight: UInt64,
        hotkeyAddress: String?,
        delegatedWeight: UInt64?,
        proofGenerated: Bool
    ) {
        self.roundId = roundId
        self.phase = phase
        self.snapshotHeight = snapshotHeight
        self.hotkeyAddress = hotkeyAddress
        self.delegatedWeight = delegatedWeight
        self.proofGenerated = proofGenerated
    }
}

public struct RoundSummaryInfo: Equatable, Sendable {
    public let roundId: String
    public let phase: RoundPhaseInfo
    public let snapshotHeight: UInt64
    public let createdAt: UInt64

    public init(roundId: String, phase: RoundPhaseInfo, snapshotHeight: UInt64, createdAt: UInt64) {
        self.roundId = roundId
        self.phase = phase
        self.snapshotHeight = snapshotHeight
        self.createdAt = createdAt
    }
}

// MARK: - Vote Record (from Rust votes table)

public struct VoteRecord: Equatable, Sendable {
    public let proposalId: UInt32
    public let bundleIndex: UInt32
    public let choice: VoteChoice
    public let submitted: Bool

    public init(proposalId: UInt32, bundleIndex: UInt32, choice: VoteChoice, submitted: Bool) {
        self.proposalId = proposalId
        self.bundleIndex = bundleIndex
        self.choice = choice
        self.submitted = submitted
    }
}

/// Combined DB state published via stateStream. Drives all UI state.
public struct VotingDbState: Equatable, Sendable {
    public let roundState: RoundStateInfo
    public let votes: [VoteRecord]

    public init(roundState: RoundStateInfo, votes: [VoteRecord]) {
        self.roundState = roundState
        self.votes = votes
    }

    /// Convenience: build the votes dictionary the UI needs.
    /// With multi-bundle, multiple VoteRecords may exist per proposal (one per bundle).
    /// A proposal is only considered "voted" when ALL of its bundle votes are submitted.
    public var votesByProposal: [UInt32: VoteChoice] {
        // Group vote records by proposal
        var byProposal: [UInt32: [VoteRecord]] = [:]
        for vote in votes {
            byProposal[vote.proposalId, default: []].append(vote)
        }
        // Only include proposals where every bundle's vote has been submitted
        var result: [UInt32: VoteChoice] = [:]
        for (proposalId, records) in byProposal where records.allSatisfy(\.submitted) {
            result[proposalId] = records.first?.choice
        }
        return result
    }

    public static let initial = VotingDbState(
        roundState: RoundStateInfo(
            roundId: "",
            phase: .initialized,
            snapshotHeight: 0,
            hotkeyAddress: nil,
            delegatedWeight: nil,
            proofGenerated: false
        ),
        votes: []
    )
}

// MARK: - Hotkey

public struct VotingHotkey: Equatable, Sendable {
    public let secretKey: Data
    public let publicKey: Data
    public let address: String

    public init(secretKey: Data, publicKey: Data, address: String) {
        self.secretKey = secretKey
        self.publicKey = publicKey
        self.address = address
    }
}

// MARK: - Governance PCZT

/// Result of building a governance-specific PCZT for Keystone signing.
/// Contains the serialized PCZT bytes plus all metadata needed for ZKP #1 witness construction.
public struct GovernancePcztResult: Equatable, Sendable {
    /// Serialized PCZT bytes for UR-encoding and Keystone signing.
    public let pcztBytes: Data
    /// Randomized verification key (32 bytes).
    public let rk: Data // swiftlint:disable:this identifier_name
    /// Spend auth randomizer scalar (32 bytes).
    public let alpha: Data
    /// Signed note nullifier (32 bytes). Public input to ZKP #1.
    public let nfSigned: Data
    /// Output note commitment (32 bytes). Public input to ZKP #1.
    public let cmxNew: Data
    /// Governance nullifiers, always padded to 5.
    public let govNullifiers: [Data]
    /// 32-byte governance commitment (VAN).
    public let van: Data
    /// 32-byte blinding factor used for VAN.
    public let vanCommRand: Data
    /// Random nullifiers used for padded dummy notes.
    public let dummyNullifiers: [Data]
    /// Constrained rho for the signed note (32 bytes).
    public let rhoSigned: Data
    /// Extracted note commitments (cmx) for padded dummy notes.
    public let paddedCmx: [Data]
    /// Signed note rseed (32 bytes).
    public let rseedSigned: Data
    /// Output note rseed (32 bytes).
    public let rseedOutput: Data
    /// Canonical delegation action payload for cosmos chain submission.
    public let actionBytes: Data
    /// Index of the governance action within the PCZT's Orchard bundle.
    public let actionIndex: UInt32

    public init(
        pcztBytes: Data,
        rk: Data, // swiftlint:disable:this identifier_name
        alpha: Data,
        nfSigned: Data,
        cmxNew: Data,
        govNullifiers: [Data],
        van: Data,
        vanCommRand: Data,
        dummyNullifiers: [Data],
        rhoSigned: Data,
        paddedCmx: [Data],
        rseedSigned: Data,
        rseedOutput: Data,
        actionBytes: Data,
        actionIndex: UInt32
    ) {
        self.pcztBytes = pcztBytes
        self.rk = rk
        self.alpha = alpha
        self.nfSigned = nfSigned
        self.cmxNew = cmxNew
        self.govNullifiers = govNullifiers
        self.van = van
        self.vanCommRand = vanCommRand
        self.dummyNullifiers = dummyNullifiers
        self.rhoSigned = rhoSigned
        self.paddedCmx = paddedCmx
        self.rseedSigned = rseedSigned
        self.rseedOutput = rseedOutput
        self.actionBytes = actionBytes
        self.actionIndex = actionIndex
    }
}

// MARK: - Delegation

/// Intermediate client-side type: the built action before proof generation.
public struct DelegationAction: Equatable, Sendable {
    public let actionBytes: Data
    public let rk: Data // swiftlint:disable:this identifier_name
    /// Governance nullifiers, always padded to 5.
    public let govNullifiers: [Data]
    /// 32-byte governance commitment (VAN).
    public let van: Data
    /// 32-byte blinding factor used for VAN.
    public let vanCommRand: Data
    /// Random nullifiers used for padded dummy notes (needed for circuit witness).
    public let dummyNullifiers: [Data]
    /// Constrained rho for the signed note (32 bytes). Spec §1.3.4.1.
    public let rhoSigned: Data
    /// Extracted note commitments (cmx) for padded dummy notes.
    public let paddedCmx: [Data]
    /// Signed note nullifier (32 bytes). Public input to ZKP #1.
    public let nfSigned: Data
    /// Output note commitment (32 bytes). Public input to ZKP #1.
    public let cmxNew: Data
    /// Spend auth randomizer scalar (32 bytes). Needed for Keystone signing.
    public let alpha: Data
    /// Signed note rseed (32 bytes). Needed for witness reconstruction.
    public let rseedSigned: Data
    /// Output note rseed (32 bytes). Needed for witness reconstruction.
    public let rseedOutput: Data
    /// Spend authorization signature returned from Keystone over the delegation dummy action.
    public let spendAuthSig: Data?

    public init(
        actionBytes: Data,
        rk: Data, // swiftlint:disable:this identifier_name
        govNullifiers: [Data],
        van: Data,
        vanCommRand: Data,
        dummyNullifiers: [Data],
        rhoSigned: Data,
        paddedCmx: [Data],
        nfSigned: Data,
        cmxNew: Data,
        alpha: Data,
        rseedSigned: Data,
        rseedOutput: Data,
        spendAuthSig: Data? = nil
    ) {
        self.actionBytes = actionBytes
        self.rk = rk
        self.govNullifiers = govNullifiers
        self.van = van
        self.vanCommRand = vanCommRand
        self.dummyNullifiers = dummyNullifiers
        self.rhoSigned = rhoSigned
        self.paddedCmx = paddedCmx
        self.nfSigned = nfSigned
        self.cmxNew = cmxNew
        self.alpha = alpha
        self.rseedSigned = rseedSigned
        self.rseedOutput = rseedOutput
        self.spendAuthSig = spendAuthSig
    }
}

/// Maps to MsgDelegateVote (zvote/v1/tx.proto).
/// All fields needed for the on-chain delegation transaction.
public struct DelegationRegistration: Equatable, Sendable {
    public let rk: Data // swiftlint:disable:this identifier_name
    public let spendAuthSig: Data
    public let signedNoteNullifier: Data
    public let cmxNew: Data
    public let vanCmx: Data
    public let govNullifiers: [Data]
    public let proof: Data
    public let voteRoundId: Data
    public let sighash: Data

    public init(
        rk: Data, // swiftlint:disable:this identifier_name
        spendAuthSig: Data,
        signedNoteNullifier: Data,
        cmxNew: Data,
        vanCmx: Data,
        govNullifiers: [Data],
        proof: Data,
        voteRoundId: Data,
        sighash: Data
    ) {
        self.rk = rk
        self.spendAuthSig = spendAuthSig
        self.signedNoteNullifier = signedNoteNullifier
        self.cmxNew = cmxNew
        self.vanCmx = vanCmx
        self.govNullifiers = govNullifiers
        self.proof = proof
        self.voteRoundId = voteRoundId
        self.sighash = sighash
    }
}

// MARK: - Voting

public struct EncryptedShare: Equatable, Sendable {
    public let c1: Data // swiftlint:disable:this identifier_name
    public let c2: Data // swiftlint:disable:this identifier_name
    public let shareIndex: UInt32
    public let plaintextValue: UInt64
    /// El Gamal randomness (32 bytes). Witness-only; must NOT be sent over the network.
    public let randomness: Data

    // swiftlint:disable:next identifier_name
    public init(c1: Data, c2: Data, shareIndex: UInt32, plaintextValue: UInt64, randomness: Data) {
        self.c1 = c1
        self.c2 = c2
        self.shareIndex = shareIndex
        self.plaintextValue = plaintextValue
        self.randomness = randomness
    }
}

/// Maps to MsgCastVote (zvote/v1/tx.proto).
public struct VoteCommitmentBundle: Equatable, Sendable {
    public let vanNullifier: Data
    public let voteAuthorityNoteNew: Data
    public let voteCommitment: Data
    public let proposalId: UInt32
    public let proof: Data
    /// Encrypted shares generated by the ZKP #2 builder (5 shares).
    /// These are the exact ciphertexts committed in the vote commitment hash
    /// and must be used for reveal-share payloads.
    public let encShares: [EncryptedShare]
    /// Tree anchor height used for the proof.
    public let anchorHeight: UInt32
    /// Voting round ID (hex string).
    public let voteRoundId: String
    /// Poseidon hash of encrypted share x-coordinates (32 bytes).
    public let sharesHash: Data
    /// Per-share blind factors (N x 32 bytes).
    public let shareBlindFactors: [Data]
    /// Pre-computed per-share Poseidon commitments (N x 32 bytes).
    public let shareComms: [Data]
    /// Compressed r_vpk (32 bytes) for sighash computation and signature verification.
    public let rVpkBytes: Data
    /// Spend-auth randomizer alpha_v (32 bytes, LE scalar repr).
    /// Needed to sign the TX2 sighash: rsk_v = ask_v.randomize(&alpha_v).
    public let alphaV: Data

    public init(
        vanNullifier: Data,
        voteAuthorityNoteNew: Data,
        voteCommitment: Data,
        proposalId: UInt32,
        proof: Data,
        encShares: [EncryptedShare],
        anchorHeight: UInt32,
        voteRoundId: String,
        sharesHash: Data,
        shareBlindFactors: [Data] = [],
        shareComms: [Data] = [],
        rVpkBytes: Data = Data(),
        alphaV: Data = Data()
    ) {
        self.vanNullifier = vanNullifier
        self.voteAuthorityNoteNew = voteAuthorityNoteNew
        self.voteCommitment = voteCommitment
        self.proposalId = proposalId
        self.proof = proof
        self.encShares = encShares
        self.anchorHeight = anchorHeight
        self.voteRoundId = voteRoundId
        self.sharesHash = sharesHash
        self.shareBlindFactors = shareBlindFactors
        self.shareComms = shareComms
        self.rVpkBytes = rVpkBytes
        self.alphaV = alphaV
    }
}

/// Payload sent to helper servers for share delegation (not directly to chain).
public struct SharePayload: Equatable, Sendable {
    public let sharesHash: Data
    public let proposalId: UInt32
    public let voteDecision: UInt32
    public let encShare: EncryptedShare
    public let treePosition: UInt64
    /// Pre-computed per-share Poseidon commitments (N x 32 bytes).
    public let shareComms: [Data]
    /// Blind factor for this specific share (32 bytes).
    public let primaryBlind: Data

    public init(sharesHash: Data, proposalId: UInt32, voteDecision: UInt32, encShare: EncryptedShare, treePosition: UInt64, shareComms: [Data] = [], primaryBlind: Data = Data()) {
        self.sharesHash = sharesHash
        self.proposalId = proposalId
        self.voteDecision = voteDecision
        self.encShare = encShare
        self.treePosition = treePosition
        self.shareComms = shareComms
        self.primaryBlind = primaryBlind
    }
}

/// Merkle witness for the VAN in the vote commitment tree.
/// Generated by syncing the tree from chain and computing the authentication path.
public struct VanWitness: Equatable, Sendable {
    /// 24 sibling hashes (32 bytes each) from leaf to root.
    public let authPath: [Data]
    /// Leaf position of the VAN in the tree.
    public let position: UInt32
    /// Block height at which the tree was snapshotted.
    public let anchorHeight: UInt32

    public init(authPath: [Data], position: UInt32, anchorHeight: UInt32) {
        self.authPath = authPath
        self.position = position
        self.anchorHeight = anchorHeight
    }
}

// MARK: - Tree & Transactions

/// Maps to CommitmentTreeState (zvote/v1/types.proto).
public struct CommitmentTreeState: Equatable, Sendable {
    public let nextIndex: UInt64
    public let root: Data
    public let height: UInt64

    public init(nextIndex: UInt64, root: Data, height: UInt64) {
        self.nextIndex = nextIndex
        self.root = root
        self.height = height
    }
}

/// Computed signature fields for cast-vote TX submission.
/// Returned by signCastVote after ZKP #2 builds the vote commitment bundle.
/// The sighash is computed on-chain from message fields; the client only
/// provides the signature (which was signed over the same sighash).
public struct CastVoteSignature: Equatable, Sendable {
    public let voteAuthSig: Data

    public init(voteAuthSig: Data) {
        self.voteAuthSig = voteAuthSig
    }
}

/// Result of checking whether a TX has been included in a block.
/// Returned by the /zally/v1/tx/{hash} endpoint.
public struct TxConfirmation: Equatable, Sendable {
    public let height: UInt64
    public let code: UInt32
    public let log: String

    public init(height: UInt64, code: UInt32, log: String = "") {
        self.height = height
        self.code = code
        self.log = log
    }
}

/// Maps to BroadcastResult from the REST API (api/handler.go).
public struct TxResult: Equatable, Sendable {
    public let txHash: String
    public let code: UInt32
    public let log: String

    public init(txHash: String, code: UInt32, log: String = "") {
        self.txHash = txHash
        self.code = code
        self.log = log
    }
}

/// Maps to QueryProposalTallyResponse (zvote/v1/query.proto).
/// Chain returns map<uint32, uint64> (vote_decision → accumulated amount).
public struct TallyResult: Equatable, Sendable {
    public struct Entry: Equatable, Sendable {
        public let decision: UInt32
        public let amount: UInt64

        public init(decision: UInt32, amount: UInt64) {
            self.decision = decision
            self.amount = amount
        }
    }

    public let entries: [Entry]

    public init(entries: [Entry]) {
        self.entries = entries
    }
}

// MARK: - Bundle Setup

/// Result of value-aware note bundling.
/// Only bundles meeting the ballot-divisor threshold are created;
/// eligible_weight reflects the total of surviving bundles.
public struct BundleSetupResult: Equatable, Sendable {
    public let bundleCount: UInt32
    public let eligibleWeight: UInt64

    public init(bundleCount: UInt32, eligibleWeight: UInt64) {
        self.bundleCount = bundleCount
        self.eligibleWeight = eligibleWeight
    }
}

// MARK: - Notes

public struct NoteInfo: Equatable, Sendable {
    public let commitment: Data
    public let nullifier: Data
    public let value: UInt64
    public let position: UInt64
    public let diversifier: Data
    public let rho: Data
    public let rseed: Data
    public let scope: UInt32
    public let ufvkStr: String

    public init(
        commitment: Data,
        nullifier: Data,
        value: UInt64,
        position: UInt64,
        diversifier: Data,
        rho: Data,
        rseed: Data,
        scope: UInt32,
        ufvkStr: String
    ) {
        self.commitment = commitment
        self.nullifier = nullifier
        self.value = value
        self.position = position
        self.diversifier = diversifier
        self.rho = rho
        self.rseed = rseed
        self.scope = scope
        self.ufvkStr = ufvkStr
    }
}

// MARK: - Witnesses

public struct WitnessData: Equatable, Sendable {
    public let noteCommitment: Data
    public let position: UInt64
    public let root: Data
    public let authPath: [Data]

    public init(noteCommitment: Data, position: UInt64, root: Data, authPath: [Data]) {
        self.noteCommitment = noteCommitment
        self.position = position
        self.root = root
        self.authPath = authPath
    }
}

// MARK: - Proof Events

public enum ProofEvent: Equatable, Sendable {
    case progress(Double)
    case completed(Data)
}

/// Streaming events for vote commitment build (ZKP #2).
/// Keeps bundle payloads separate from generic proof streams used elsewhere.
public enum VoteCommitmentBuildEvent: Equatable, Sendable {
    case progress(Double)
    case completed(VoteCommitmentBundle)
}
