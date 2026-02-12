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
            generateHotkey: { roundId in
                let db = try await dbActor.database()
                let hotkey = try db.generateHotkey(roundId: roundId)
                return VotingHotkey(
                    secretKey: hotkey.secretKey,
                    publicKey: hotkey.publicKey,
                    address: hotkey.address
                )
            },
            constructDelegationAction: { roundId, hotkey, notes in
                let db = try await dbActor.database()
                let ffiHotkey = ZcashVotingFFI.VotingHotkey(
                    secretKey: hotkey.secretKey,
                    publicKey: hotkey.publicKey,
                    address: hotkey.address
                )
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
                    hotkey: ffiHotkey,
                    notes: ffiNotes
                )
                return DelegationAction(
                    actionBytes: result.actionBytes,
                    rk: result.rk,
                    sighash: result.sighash
                )
            },
            storeTreeState: { roundId, treeState in
                let db = try await dbActor.database()
                try db.storeTreeState(roundId: roundId, treeStateBytes: treeState)
            },
            buildDelegationWitness: { roundId, action, inclusionProofs, exclusionProofs in
                let db = try await dbActor.database()
                let ffiAction = ZcashVotingFFI.DelegationAction(
                    actionBytes: action.actionBytes,
                    rk: action.rk,
                    sighash: action.sighash
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
                            let reporter = StreamProgressReporter(continuation)
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
                                voteRoundId: Data(repeating: 0, count: 32),
                                voteCommTreeAnchorHeight: 0
                            )
                            continuation.yield(.completed(bundle.proof))
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

enum VotingCryptoError: Error {
    case proofFailed(String)
    case databaseNotOpen
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

private extension Data {
    var hexString: String {
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
