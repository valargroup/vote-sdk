import Combine
import ComposableArchitecture
import Foundation
import VotingModels
import ZcashVotingFFI

// MARK: - StreamProgressReporter

/// Bridges UniFFI ProofProgressReporter callback → AsyncThrowingStream<ProofEvent>.
private final class StreamProgressReporter: ZcashVotingFFI.ProofProgressReporter {
    let continuation: AsyncThrowingStream<ProofEvent, Error>.Continuation

    init(_ continuation: AsyncThrowingStream<ProofEvent, Error>.Continuation) {
        self.continuation = continuation
    }

    func onProgress(progress: Double) {
        continuation.yield(.progress(progress))
    }
}

/// Bridges UniFFI ProofProgressReporter callback for vote commitment streams.
private final class VoteCommitmentProgressReporter: ZcashVotingFFI.ProofProgressReporter {
    let continuation: AsyncThrowingStream<VoteCommitmentBuildEvent, Error>.Continuation

    init(_ continuation: AsyncThrowingStream<VoteCommitmentBuildEvent, Error>.Continuation) {
        self.continuation = continuation
    }

    func onProgress(progress: Double) {
        continuation.yield(.progress(progress))
    }
}

// MARK: - Live key

extension VotingCryptoClient: DependencyKey {
    public static var liveValue: Self {
        let dbActor = DatabaseActor()
        let stateSubject = CurrentValueSubject<VotingDbState, Never>(.initial)

        /// Query rounds + votes tables and publish combined state.
        func publishState(db: ZcashVotingFFI.VotingDatabase, roundId: String) {
            guard let roundState = try? db.getRoundState(roundId: roundId) else { return }
            let ffiVotes = (try? db.getVotes(roundId: roundId)) ?? []
            let dbState = VotingDbState(
                roundState: RoundStateInfo(
                    roundId: roundState.roundId,
                    phase: roundState.phase.toModel(),
                    snapshotHeight: roundState.snapshotHeight,
                    hotkeyAddress: roundState.hotkeyAddress,
                    delegatedWeight: roundState.delegatedWeight,
                    proofGenerated: roundState.proofGenerated
                ),
                votes: ffiVotes.map { $0.toModel() }
            )
            stateSubject.send(dbState)
        }

        return Self(
            stateStream: {
                stateSubject
                    .dropFirst() // Skip initial empty state
                    .eraseToAnyPublisher()
            },
            openDatabase: { path in
                try await dbActor.open(path: path)
            },
            initRound: { params, sessionJson in
                let db = try await dbActor.database()
                let ffiParams = ZcashVotingFFI.VotingRoundParams(
                    voteRoundId: params.voteRoundId.hexString,
                    snapshotHeight: params.snapshotHeight,
                    eaPk: params.eaPK,
                    ncRoot: params.ncRoot,
                    nullifierImtRoot: params.nullifierIMTRoot
                )
                try db.initRound(params: ffiParams, sessionJson: sessionJson)
                publishState(db: db, roundId: params.voteRoundId.hexString)
            },
            getRoundState: { roundId in
                let db = try await dbActor.database()
                let state = try db.getRoundState(roundId: roundId)
                return RoundStateInfo(
                    roundId: state.roundId,
                    phase: state.phase.toModel(),
                    snapshotHeight: state.snapshotHeight,
                    hotkeyAddress: state.hotkeyAddress,
                    delegatedWeight: state.delegatedWeight,
                    proofGenerated: state.proofGenerated
                )
            },
            getVotes: { roundId in
                let db = try await dbActor.database()
                let ffiVotes = try db.getVotes(roundId: roundId)
                return ffiVotes.map { $0.toModel() }
            },
            listRounds: {
                let db = try await dbActor.database()
                return try db.listRounds().map {
                    RoundSummaryInfo(
                        roundId: $0.roundId,
                        phase: $0.phase.toModel(),
                        snapshotHeight: $0.snapshotHeight,
                        createdAt: $0.createdAt
                    )
                }
            },
            clearRound: { roundId in
                let db = try await dbActor.database()
                try db.clearRound(roundId: roundId)
            },
            getWalletNotes: { walletDbPath, snapshotHeight, networkId in
                let db = try await dbActor.database()
                let ffiNotes = try db.getWalletNotes(
                    walletDbPath: walletDbPath,
                    snapshotHeight: snapshotHeight,
                    networkId: networkId
                )
                return ffiNotes.map {
                    NoteInfo(
                        commitment: $0.commitment,
                        nullifier: $0.nullifier,
                        value: $0.value,
                        position: $0.position
                    )
                }
            },
            generateNoteWitnesses: { roundId, walletDbPath, notes in
                let db = try await dbActor.database()
                let ffiNotes = notes.map {
                    ZcashVotingFFI.NoteInfo(
                        commitment: $0.commitment,
                        nullifier: $0.nullifier,
                        value: $0.value,
                        position: $0.position
                    )
                }
                let ffiWitnesses = try db.generateNoteWitnesses(
                    roundId: roundId,
                    walletDbPath: walletDbPath,
                    notes: ffiNotes
                )
                return ffiWitnesses.map {
                    WitnessData(
                        noteCommitment: $0.noteCommitment,
                        position: $0.position,
                        root: $0.root,
                        authPath: $0.authPath
                    )
                }
            },
            verifyWitness: { witness in
                let ffiWitness = ZcashVotingFFI.WitnessData(
                    noteCommitment: witness.noteCommitment,
                    position: witness.position,
                    root: witness.root,
                    authPath: witness.authPath
                )
                return try ZcashVotingFFI.verifyWitness(witness: ffiWitness)
            },
            generateHotkey: { roundId, seed in
                let db = try await dbActor.database()
                let hotkey = try db.generateHotkey(roundId: roundId, seed: Data(seed))
                return VotingModels.VotingHotkey(
                    secretKey: hotkey.secretKey,
                    publicKey: hotkey.publicKey,
                    address: hotkey.address
                )
            },
            buildDelegationSignAction: { roundId, notes, senderSeed, hotkeySeed, networkId, accountIndex in
                let db = try await dbActor.database()
                let ffiHotkey = try db.generateHotkey(roundId: roundId, seed: Data(hotkeySeed))
                let hotkey = VotingModels.VotingHotkey(
                    secretKey: ffiHotkey.secretKey,
                    publicKey: ffiHotkey.publicKey,
                    address: ffiHotkey.address
                )
                let ffiInputs = try ZcashVotingFFI.generateDelegationInputs(
                    senderSeed: Data(senderSeed),
                    hotkeySeed: Data(hotkeySeed),
                    networkId: networkId,
                    accountIndex: accountIndex
                )
                guard hotkey.publicKey == ffiInputs.hotkeyPublicKey,
                      hotkey.address == ffiInputs.hotkeyAddress
                else {
                    throw VotingCryptoError.hotkeySeedBindingMismatch
                }
                let ffiNotes = notes.map {
                    ZcashVotingFFI.NoteInfo(
                        commitment: $0.commitment,
                        nullifier: $0.nullifier,
                        value: $0.value,
                        position: $0.position
                    )
                }
                let result = try db.constructDelegationAction(
                    roundId: roundId,
                    notes: ffiNotes,
                    fvkBytes: ffiInputs.fvkBytes,
                    gDNewX: ffiInputs.gDNewX,
                    pkDNewX: ffiInputs.pkDNewX,
                    hotkeyRawAddress: ffiInputs.hotkeyRawAddress
                )
                return DelegationAction(
                    actionBytes: result.actionBytes,
                    rk: result.rk,
                    govNullifiers: result.govNullifiers,
                    van: result.van,
                    govCommRand: result.govCommRand,
                    dummyNullifiers: result.dummyNullifiers,
                    rhoSigned: result.rhoSigned,
                    paddedCmx: result.paddedCmx,
                    nfSigned: result.nfSigned,
                    cmxNew: result.cmxNew,
                    alpha: result.alpha,
                    rseedSigned: result.rseedSigned,
                    rseedOutput: result.rseedOutput,
                    spendAuthSig: nil
                )
            },
            buildGovernancePczt: { roundId, notes, senderSeed, hotkeySeed, networkId, accountIndex, roundName in
                let db = try await dbActor.database()
                _ = try db.generateHotkey(roundId: roundId, seed: Data(hotkeySeed))
                let ffiInputs = try ZcashVotingFFI.generateDelegationInputs(
                    senderSeed: Data(senderSeed),
                    hotkeySeed: Data(hotkeySeed),
                    networkId: networkId,
                    accountIndex: accountIndex
                )
                let ffiNotes = notes.map {
                    ZcashVotingFFI.NoteInfo(
                        commitment: $0.commitment,
                        nullifier: $0.nullifier,
                        value: $0.value,
                        position: $0.position
                    )
                }
                // NU6 consensus branch ID; coin_type 133 = mainnet, 1 = testnet
                let consensusBranchId: UInt32 = 0xC8E7_1055
                let coinType: UInt32 = networkId == 0 ? 133 : 1
                // Round params are loaded from DB internally by build_governance_pczt
                let result = try db.buildGovernancePczt(
                    roundId: roundId,
                    notes: ffiNotes,
                    fvkBytes: ffiInputs.fvkBytes,
                    hotkeyRawAddress: ffiInputs.hotkeyRawAddress,
                    consensusBranchId: consensusBranchId,
                    coinType: coinType,
                    seedFingerprint: ffiInputs.seedFingerprint,
                    accountIndex: accountIndex,
                    roundName: roundName
                )
                publishState(db: db, roundId: roundId)
                return GovernancePcztResult(
                    pcztBytes: result.pcztBytes,
                    rk: result.rk,
                    alpha: result.alpha,
                    nfSigned: result.nfSigned,
                    cmxNew: result.cmxNew,
                    govNullifiers: result.govNullifiers,
                    van: result.van,
                    govCommRand: result.govCommRand,
                    dummyNullifiers: result.dummyNullifiers,
                    rhoSigned: result.rhoSigned,
                    paddedCmx: result.paddedCmx,
                    rseedSigned: result.rseedSigned,
                    rseedOutput: result.rseedOutput,
                    actionBytes: result.actionBytes,
                    actionIndex: result.actionIndex
                )
            },
            storeTreeState: { roundId, treeState in
                let db = try await dbActor.database()
                try db.storeTreeState(roundId: roundId, treeStateBytes: treeState)
            },
            buildDelegationWitness: { roundId, action, spendAuthSig, inclusionProofs, exclusionProofs in
                guard spendAuthSig.count == 64 else {
                    throw VotingCryptoError.invalidSpendAuthSignatureLength(spendAuthSig.count)
                }
                let db = try await dbActor.database()
                let ffiAction = ZcashVotingFFI.DelegationAction(
                    actionBytes: action.actionBytes,
                    rk: action.rk,
                    govNullifiers: action.govNullifiers,
                    van: action.van,
                    govCommRand: action.govCommRand,
                    dummyNullifiers: action.dummyNullifiers,
                    rhoSigned: action.rhoSigned,
                    paddedCmx: action.paddedCmx,
                    nfSigned: action.nfSigned,
                    cmxNew: action.cmxNew,
                    alpha: action.alpha,
                    spendAuthSig: spendAuthSig,
                    rseedSigned: action.rseedSigned,
                    rseedOutput: action.rseedOutput
                )
                let witness = try db.buildDelegationWitness(
                    roundId: roundId,
                    action: ffiAction,
                    inclusionProofs: inclusionProofs,
                    exclusionProofs: exclusionProofs
                )
                publishState(db: db, roundId: roundId)
                return witness
            },
            extractSpendAuthSignatureFromSignedPczt: { signedPczt, actionIndex in
                let sigBytes = try ZcashVotingFFI.extractSpendAuthSig(
                    signedPcztBytes: signedPczt,
                    actionIndex: actionIndex
                )
                return sigBytes
            },
            generateDelegationProof: { roundId in
                AsyncThrowingStream { continuation in
                    Task.detached {
                        do {
                            let db = try await dbActor.database()
                            let reporter = StreamProgressReporter(continuation)
                            let result = try db.generateDelegationProof(
                                roundId: roundId,
                                progress: reporter
                            )
                            guard result.success else {
                                continuation.finish(throwing: VotingCryptoError.proofFailed(
                                    result.error ?? "unknown"
                                ))
                                return
                            }
                            publishState(db: db, roundId: roundId)
                            continuation.yield(.completed(result.proof))
                            continuation.finish()
                        } catch {
                            continuation.finish(throwing: error)
                        }
                    }
                }
            },
            decomposeWeight: { weight in
                ZcashVotingFFI.decomposeWeight(weight: weight)
            },
            encryptShares: { roundId, shares in
                let db = try await dbActor.database()
                let ffiShares = try db.encryptShares(roundId: roundId, shares: shares)
                return ffiShares.map {
                    EncryptedShare(
                        c1: $0.c1,
                        c2: $0.c2,
                        shareIndex: $0.shareIndex,
                        plaintextValue: $0.plaintextValue
                    )
                }
            },
            buildVoteCommitment: { roundId, proposalId, choice, encShares, vanWitness in
                AsyncThrowingStream { continuation in
                    Task.detached {
                        do {
                            let db = try await dbActor.database()
                            let reporter = VoteCommitmentProgressReporter(continuation)
                            let ffiShares = encShares.map {
                                ZcashVotingFFI.EncryptedShare(
                                    c1: $0.c1,
                                    c2: $0.c2,
                                    shareIndex: $0.shareIndex,
                                    plaintextValue: $0.plaintextValue
                                )
                            }
                            let result = try db.buildVoteCommitment(
                                roundId: roundId,
                                proposalId: proposalId,
                                choice: choice.ffiValue,
                                encShares: ffiShares,
                                vanWitness: vanWitness,
                                progress: reporter
                            )
                            publishState(db: db, roundId: roundId)
                            let bundle = VoteCommitmentBundle(
                                vanNullifier: result.vanNullifier,
                                voteAuthorityNoteNew: result.voteAuthorityNoteNew,
                                voteCommitment: result.voteCommitment,
                                proposalId: proposalId,
                                proof: result.proof,
                                // TODO(gov-steps-phase-3-voting): Source this from canonical
                                // round metadata once MsgCastVote fields are fully wired.
                                voteRoundId: Data(repeating: 0, count: 32),
                                // TODO(gov-steps-phase-3-voting): Replace with vote commitment
                                // tree anchor height, not a local placeholder.
                                voteCommTreeAnchorHeight: 0
                            )
                            continuation.yield(.completed(bundle))
                            continuation.finish()
                        } catch {
                            continuation.finish(throwing: error)
                        }
                    }
                }
            },
            buildSharePayloads: { encShares, commitment in
                let db = try await dbActor.database()
                let ffiShares = encShares.map {
                    ZcashVotingFFI.EncryptedShare(
                        c1: $0.c1,
                        c2: $0.c2,
                        shareIndex: $0.shareIndex,
                        plaintextValue: $0.plaintextValue
                    )
                }
                let ffiCommitment = ZcashVotingFFI.VoteCommitmentBundle(
                    vanNullifier: commitment.vanNullifier,
                    voteAuthorityNoteNew: commitment.voteAuthorityNoteNew,
                    voteCommitment: commitment.voteCommitment,
                    proposalId: commitment.proposalId,
                    proof: commitment.proof
                )
                let ffiPayloads = try db.buildSharePayloads(
                    encShares: ffiShares,
                    commitment: ffiCommitment
                )
                return ffiPayloads.map {
                    SharePayload(
                        sharesHash: $0.sharesHash,
                        proposalId: commitment.proposalId,
                        voteDecision: $0.voteDecision,
                        encShare: EncryptedShare(
                            c1: $0.encShare.c1,
                            c2: $0.encShare.c2,
                            shareIndex: $0.encShare.shareIndex,
                            plaintextValue: $0.encShare.plaintextValue
                        ),
                        shareIndex: $0.encShare.shareIndex,
                        treePosition: $0.treePosition
                    )
                }
            },
            markVoteSubmitted: { roundId, proposalId in
                let db = try await dbActor.database()
                try db.markVoteSubmitted(roundId: roundId, proposalId: proposalId)
                publishState(db: db, roundId: roundId)
            }
        )
    }
}

// MARK: - DatabaseActor

/// Thread-safe holder for the VotingDatabase instance.
private actor DatabaseActor {
    private var db: ZcashVotingFFI.VotingDatabase?

    func open(path: String) throws {
        db = try ZcashVotingFFI.VotingDatabase.open(path: path)
    }

    func database() throws -> ZcashVotingFFI.VotingDatabase {
        guard let db else {
            throw VotingCryptoError.databaseNotOpen
        }
        return db
    }
}

// MARK: - Helpers

enum VotingCryptoError: LocalizedError {
    case proofFailed(String)
    case databaseNotOpen
    case hotkeySeedBindingMismatch
    case invalidSpendAuthSignatureLength(Int)

    var errorDescription: String? {
        switch self {
        case .proofFailed(let reason):
            return "Delegation proof generation failed: \(reason)"
        case .databaseNotOpen:
            return "Voting database is not open."
        case .hotkeySeedBindingMismatch:
            return "Hotkey derivation mismatch while building delegation sign action."
        case .invalidSpendAuthSignatureLength(let actual):
            return "SpendAuthSig must be 64 bytes, got \(actual)."
        }
    }
}


private extension VoteChoice {
    var ffiValue: UInt32 {
        switch self {
        case .support: return 0
        case .oppose: return 1
        case .skip: return 2
        }
    }

    static func fromFFI(_ value: UInt32) -> VoteChoice {
        switch value {
        case 0: return .support
        case 1: return .oppose
        default: return .skip
        }
    }
}

public extension Data {
    public var hexString: String {
        map { String(format: "%02x", $0) }.joined()
    }
}

private extension ZcashVotingFFI.RoundPhase {
    func toModel() -> RoundPhaseInfo {
        switch self {
        case .initialized: return .initialized
        case .hotkeyGenerated: return .hotkeyGenerated
        case .delegationConstructed: return .delegationConstructed
        case .witnessBuilt: return .witnessBuilt
        case .delegationProved: return .delegationProved
        case .voteReady: return .voteReady
        }
    }
}

private extension ZcashVotingFFI.VoteRecord {
    func toModel() -> VotingModels.VoteRecord {
        VotingModels.VoteRecord(
            proposalId: proposalId,
            choice: VoteChoice.fromFFI(choice),
            submitted: submitted
        )
    }
}
