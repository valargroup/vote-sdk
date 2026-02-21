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
            refreshState: { roundId in
                guard let db = try? await dbActor.database() else { return }
                publishState(db: db, roundId: roundId)
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
                        position: $0.position,
                        diversifier: $0.diversifier,
                        rho: $0.rho,
                        rseed: $0.rseed,
                        scope: $0.scope,
                        ufvkStr: $0.ufvkStr
                    )
                }
            },
            setupBundles: { roundId, notes in
                let db = try await dbActor.database()
                let ffiNotes = notes.map { $0.toFFI() }
                let ffiResult = try db.setupBundles(roundId: roundId, notes: ffiNotes)
                return BundleSetupResult(
                    bundleCount: ffiResult.bundleCount,
                    eligibleWeight: ffiResult.eligibleWeight
                )
            },
            getBundleCount: { roundId in
                let db = try await dbActor.database()
                return try db.getBundleCount(roundId: roundId)
            },
            generateNoteWitnesses: { roundId, bundleIndex, walletDbPath, notes in
                let db = try await dbActor.database()
                let ffiNotes = notes.map { $0.toFFI() }
                let ffiWitnesses = try db.generateNoteWitnesses(
                    roundId: roundId,
                    bundleIndex: bundleIndex,
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
            buildDelegationSignAction: { roundId, bundleIndex, notes, senderSeed, hotkeySeed, networkId, accountIndex in
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
                let ffiNotes = notes.map { $0.toFFI() }
                let result = try db.constructDelegationAction(
                    roundId: roundId,
                    bundleIndex: bundleIndex,
                    notes: ffiNotes,
                    fvkBytes: ffiInputs.fvkBytes,
                    gDNewX: ffiInputs.gDNewX,
                    pkDNewX: ffiInputs.pkDNewX,
                    hotkeyRawAddress: ffiInputs.hotkeyRawAddress,
                    addressIndex: 1
                )
                return DelegationAction(
                    actionBytes: result.actionBytes,
                    rk: result.rk,
                    govNullifiers: result.govNullifiers,
                    van: result.van,
                    vanCommRand: result.vanCommRand,
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
            buildGovernancePczt: { roundId, bundleIndex, notes, senderSeed, hotkeySeed, networkId, accountIndex, roundName in
                let db = try await dbActor.database()
                _ = try db.generateHotkey(roundId: roundId, seed: Data(hotkeySeed))
                let ffiInputs = try ZcashVotingFFI.generateDelegationInputs(
                    senderSeed: Data(senderSeed),
                    hotkeySeed: Data(hotkeySeed),
                    networkId: networkId,
                    accountIndex: accountIndex
                )
                let ffiNotes = notes.map { $0.toFFI() }
                // NU6 consensus branch ID; coin_type 133 = mainnet, 1 = testnet
                let consensusBranchId: UInt32 = 0xC8E7_1055
                let coinType: UInt32 = networkId == 0 ? 133 : 1
                // Round params are loaded from DB internally by build_governance_pczt
                let result = try db.buildGovernancePczt(
                    roundId: roundId,
                    bundleIndex: bundleIndex,
                    notes: ffiNotes,
                    fvkBytes: ffiInputs.fvkBytes,
                    hotkeyRawAddress: ffiInputs.hotkeyRawAddress,
                    consensusBranchId: consensusBranchId,
                    coinType: coinType,
                    seedFingerprint: ffiInputs.seedFingerprint,
                    accountIndex: accountIndex,
                    roundName: roundName,
                    addressIndex: 1
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
                    vanCommRand: result.vanCommRand,
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
            extractSpendAuthSignatureFromSignedPczt: { signedPczt, actionIndex in
                let sigBytes = try ZcashVotingFFI.extractSpendAuthSig(
                    signedPcztBytes: signedPczt,
                    actionIndex: actionIndex
                )
                return sigBytes
            },
            buildAndProveDelegation: { roundId, bundleIndex, bundleNotes, walletDbPath, senderSeed, hotkeySeed, networkId, accountIndex, imtServerUrl in
                AsyncThrowingStream { continuation in
                    Task.detached {
                        do {
                            let db = try await dbActor.database()
                            let reporter = StreamProgressReporter(continuation)
                            // Derive hotkey raw address from seeds
                            let ffiInputs = try ZcashVotingFFI.generateDelegationInputs(
                                senderSeed: Data(senderSeed),
                                hotkeySeed: Data(hotkeySeed),
                                networkId: networkId,
                                accountIndex: accountIndex
                            )
                            // Construct delegation action for this bundle's notes
                            let ffiNotes = bundleNotes.map { $0.toFFI() }
                            _ = try db.constructDelegationAction(
                                roundId: roundId,
                                bundleIndex: bundleIndex,
                                notes: ffiNotes,
                                fvkBytes: ffiInputs.fvkBytes,
                                gDNewX: ffiInputs.gDNewX,
                                pkDNewX: ffiInputs.pkDNewX,
                                hotkeyRawAddress: ffiInputs.hotkeyRawAddress,
                                addressIndex: accountIndex
                            )
                            let result = try db.buildAndProveDelegation(
                                roundId: roundId,
                                bundleIndex: bundleIndex,
                                walletDbPath: walletDbPath,
                                hotkeyRawAddress: ffiInputs.hotkeyRawAddress,
                                imtServerUrl: imtServerUrl,
                                networkId: networkId,
                                progress: reporter
                            )
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
                        plaintextValue: $0.plaintextValue,
                        randomness: $0.randomness
                    )
                }
            },
            buildVoteCommitment: { roundId, bundleIndex, hotkeySeed, networkId, proposalId, choice, numOptions, vanAuthPath, vanPosition, anchorHeight in
                AsyncThrowingStream { continuation in
                    Task.detached {
                        do {
                            let db = try await dbActor.database()
                            let reporter = VoteCommitmentProgressReporter(continuation)
                            let result = try db.buildVoteCommitment(
                                roundId: roundId,
                                bundleIndex: bundleIndex,
                                hotkeySeed: Data(hotkeySeed),
                                networkId: networkId,
                                proposalId: proposalId,
                                choice: choice.ffiValue,
                                numOptions: numOptions,
                                vanAuthPath: vanAuthPath,
                                vanPosition: vanPosition,
                                anchorHeight: anchorHeight,
                                progress: reporter
                            )
                            publishState(db: db, roundId: roundId)
                            let bundle = VoteCommitmentBundle(
                                vanNullifier: result.vanNullifier,
                                voteAuthorityNoteNew: result.voteAuthorityNoteNew,
                                voteCommitment: result.voteCommitment,
                                proposalId: proposalId,
                                proof: result.proof,
                                encShares: result.encShares.map {
                                    VotingModels.EncryptedShare(
                                        c1: $0.c1, c2: $0.c2,
                                        shareIndex: $0.shareIndex,
                                        plaintextValue: $0.plaintextValue,
                                        randomness: $0.randomness
                                    )
                                },
                                anchorHeight: result.anchorHeight,
                                voteRoundId: result.voteRoundId,
                                sharesHash: result.sharesHash,
                                rVpkBytes: result.rVpkBytes,
                                alphaV: result.alphaV
                            )
                            continuation.yield(.completed(bundle))
                            continuation.finish()
                        } catch {
                            continuation.finish(throwing: error)
                        }
                    }
                }
            },
            buildSharePayloads: { encShares, commitment, voteDecision, numOptions, vcTreePosition in
                let db = try await dbActor.database()
                let ffiShares = encShares.map {
                    ZcashVotingFFI.EncryptedShare(
                        c1: $0.c1,
                        c2: $0.c2,
                        shareIndex: $0.shareIndex,
                        plaintextValue: $0.plaintextValue,
                        randomness: $0.randomness
                    )
                }
                let ffiCommitment = ZcashVotingFFI.VoteCommitmentBundle(
                    vanNullifier: commitment.vanNullifier,
                    voteAuthorityNoteNew: commitment.voteAuthorityNoteNew,
                    voteCommitment: commitment.voteCommitment,
                    proposalId: commitment.proposalId,
                    proof: commitment.proof,
                    encShares: ffiShares,
                    anchorHeight: commitment.anchorHeight,
                    voteRoundId: commitment.voteRoundId,
                    sharesHash: commitment.sharesHash,
                    rVpkBytes: commitment.rVpkBytes,
                    alphaV: commitment.alphaV
                )
                let ffiPayloads = try db.buildSharePayloads(
                    encShares: ffiShares,
                    commitment: ffiCommitment,
                    voteDecision: voteDecision.ffiValue,
                    numOptions: numOptions,
                    vcTreePosition: vcTreePosition
                )
                return ffiPayloads.map {
                    SharePayload(
                        sharesHash: $0.sharesHash,
                        proposalId: $0.proposalId,
                        voteDecision: $0.voteDecision,
                        encShare: EncryptedShare(
                            c1: $0.encShare.c1,
                            c2: $0.encShare.c2,
                            shareIndex: $0.encShare.shareIndex,
                            plaintextValue: $0.encShare.plaintextValue,
                            randomness: $0.encShare.randomness
                        ),
                        treePosition: $0.treePosition,
                        allEncShares: $0.allEncShares.map {
                            EncryptedShare(
                                c1: $0.c1,
                                c2: $0.c2,
                                shareIndex: $0.shareIndex,
                                plaintextValue: $0.plaintextValue,
                                randomness: $0.randomness
                            )
                        }
                    )
                }
            },
            getDelegationSubmission: { roundId, bundleIndex, senderSeed, networkId, accountIndex in
                let db = try await dbActor.database()
                let ffi = try db.getDelegationSubmission(
                    roundId: roundId,
                    bundleIndex: bundleIndex,
                    senderSeed: Data(senderSeed),
                    networkId: networkId,
                    accountIndex: accountIndex
                )
                // Map FFI DelegationSubmission → VotingModels DelegationRegistration
                // voteRoundId is hex-encoded in FFI; decode to raw bytes for chain submission
                let voteRoundIdBytes = Data(hexString: ffi.voteRoundId)
                return DelegationRegistration(
                    rk: ffi.rk,
                    spendAuthSig: ffi.spendAuthSig,
                    signedNoteNullifier: ffi.nfSigned,
                    cmxNew: ffi.cmxNew,
                    encMemo: ffi.encMemo,
                    vanCmx: ffi.govComm,
                    govNullifiers: ffi.govNullifiers,
                    proof: ffi.proof,
                    voteRoundId: voteRoundIdBytes,
                    sighash: ffi.sighash
                )
            },
            storeVanPosition: { roundId, bundleIndex, position in
                let db = try await dbActor.database()
                try db.storeVanPosition(roundId: roundId, bundleIndex: bundleIndex, position: position)
            },
            syncVoteTree: { roundId, nodeUrl in
                let db = try await dbActor.database()
                return try db.syncVoteTree(roundId: roundId, nodeUrl: nodeUrl)
            },
            generateVanWitness: { roundId, bundleIndex, anchorHeight in
                let db = try await dbActor.database()
                let ffiWitness = try db.generateVanWitness(roundId: roundId, bundleIndex: bundleIndex, anchorHeight: anchorHeight)
                return VanWitness(
                    authPath: ffiWitness.authPath,
                    position: ffiWitness.position,
                    anchorHeight: ffiWitness.anchorHeight
                )
            },
            markVoteSubmitted: { roundId, bundleIndex, proposalId in
                let db = try await dbActor.database()
                try db.markVoteSubmitted(roundId: roundId, bundleIndex: bundleIndex, proposalId: proposalId)
                publishState(db: db, roundId: roundId)
            },
            signCastVote: { hotkeySeed, networkId, bundle in
                let ffiSig = try ZcashVotingFFI.signCastVote(
                    hotkeySeed: Data(hotkeySeed),
                    networkId: networkId,
                    voteRoundIdHex: bundle.voteRoundId,
                    rVpkBytes: bundle.rVpkBytes,
                    vanNullifier: bundle.vanNullifier,
                    voteAuthorityNoteNew: bundle.voteAuthorityNoteNew,
                    voteCommitment: bundle.voteCommitment,
                    proposalId: bundle.proposalId,
                    anchorHeight: bundle.anchorHeight,
                    alphaV: bundle.alphaV
                )
                return CastVoteSignature(
                    rVpkX: ffiSig.rVpkX,
                    rVpkY: ffiSig.rVpkY,
                    sighash: ffiSig.sighash,
                    voteAuthSig: ffiSig.voteAuthSig
                )
            },
            extractNcRoot: { treeStateBytes in
                try ZcashVotingFFI.extractNcRoot(treeStateBytes: treeStateBytes)
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

    /// Initialize Data from a hex-encoded string (e.g. "0a1b2c").
    init(hexString: String) {
        var data = Data()
        var hex = hexString
        while hex.count >= 2 {
            let byteString = String(hex.prefix(2))
            hex = String(hex.dropFirst(2))
            if let byte = UInt8(byteString, radix: 16) {
                data.append(byte)
            }
        }
        self = data
    }
}

private extension VotingModels.NoteInfo {
    func toFFI() -> ZcashVotingFFI.NoteInfo {
        ZcashVotingFFI.NoteInfo(
            commitment: commitment,
            nullifier: nullifier,
            value: value,
            position: position,
            diversifier: diversifier,
            rho: rho,
            rseed: rseed,
            scope: scope,
            ufvkStr: ufvkStr
        )
    }
}

private extension ZcashVotingFFI.RoundPhase {
    func toModel() -> RoundPhaseInfo {
        switch self {
        case .initialized: return .initialized
        case .hotkeyGenerated: return .hotkeyGenerated
        case .delegationConstructed: return .delegationConstructed
        case .delegationProved: return .delegationProved
        case .voteReady: return .voteReady
        }
    }
}

private extension ZcashVotingFFI.VoteRecord {
    func toModel() -> VotingModels.VoteRecord {
        VotingModels.VoteRecord(
            proposalId: proposalId,
            bundleIndex: bundleIndex,
            choice: VoteChoice.fromFFI(choice),
            submitted: submitted
        )
    }
}
