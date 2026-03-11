import Combine
import ComposableArchitecture
import Foundation
import VotingModels
import ZcashLightClientKit

// MARK: - Live key

extension VotingCryptoClient: DependencyKey {
    public static var liveValue: Self {
        let dbActor = DatabaseActor()
        let stateSubject = CurrentValueSubject<VotingDbState, Never>(.initial)

        /// Query rounds + votes tables and publish combined state.
        func publishState(backend: VotingRustBackend, roundId: String) {
            guard let roundState = try? backend.getRoundState(roundId: roundId) else { return }
            let votes = (try? backend.getVotes(roundId: roundId)) ?? []
            let dbState = VotingDbState(
                roundState: RoundStateInfo(
                    roundId: roundState.roundId,
                    phase: roundState.phase.toModel(),
                    snapshotHeight: roundState.snapshotHeight,
                    hotkeyAddress: roundState.hotkeyAddress,
                    delegatedWeight: roundState.delegatedWeight,
                    proofGenerated: roundState.proofGenerated
                ),
                votes: votes.map { $0.toModel() }
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
                guard let backend = try? await dbActor.backend() else { return }
                publishState(backend: backend, roundId: roundId)
            },
            openDatabase: { path in
                try await dbActor.open(path: path)
            },
            initRound: { params, sessionJson in
                let backend = try await dbActor.backend()
                let roundIdHex = params.voteRoundId.hexString
                try backend.initRound(
                    roundId: roundIdHex,
                    snapshotHeight: params.snapshotHeight,
                    eaPk: [UInt8](params.eaPK),
                    ncRoot: [UInt8](params.ncRoot),
                    nullifierImtRoot: [UInt8](params.nullifierIMTRoot),
                    sessionJson: sessionJson
                )
                publishState(backend: backend, roundId: roundIdHex)
            },
            getRoundState: { roundId in
                let backend = try await dbActor.backend()
                let state = try backend.getRoundState(roundId: roundId)
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
                let backend = try await dbActor.backend()
                let votes = try backend.getVotes(roundId: roundId)
                return votes.map { $0.toModel() }
            },
            listRounds: {
                let backend = try await dbActor.backend()
                return try backend.listRounds().map {
                    RoundSummaryInfo(
                        roundId: $0.roundId,
                        phase: $0.phase.toModel(),
                        snapshotHeight: $0.snapshotHeight,
                        createdAt: $0.createdAt
                    )
                }
            },
            clearRound: { roundId in
                let backend = try await dbActor.backend()
                try backend.clearRound(roundId: roundId)
            },
            deleteSkippedBundles: { roundId, keepCount in
                let backend = try await dbActor.backend()
                _ = try backend.deleteSkippedBundles(roundId: roundId, keepCount: keepCount)
            },
            getWalletNotes: { walletDbPath, snapshotHeight, networkId, seedFingerprint, accountIndex in
                let backend = try await dbActor.backend()
                let notes = try backend.getWalletNotes(
                    walletDbPath: walletDbPath,
                    snapshotHeight: snapshotHeight,
                    networkId: networkId,
                    seedFingerprint: seedFingerprint,
                    accountIndex: accountIndex.map { Int64($0) } ?? -1
                )
                return notes.map {
                    NoteInfo(
                        commitment: Data($0.commitment),
                        nullifier: Data($0.nullifier),
                        value: $0.value,
                        position: $0.position,
                        diversifier: Data($0.diversifier),
                        rho: Data($0.rho),
                        rseed: Data($0.rseed),
                        scope: $0.scope,
                        ufvkStr: $0.ufvkStr
                    )
                }
            },
            setupBundles: { roundId, notes in
                let backend = try await dbActor.backend()
                let sdkNotes = notes.map { $0.toSDK() }
                let result = try backend.setupBundles(roundId: roundId, notes: sdkNotes)
                return BundleSetupResult(
                    bundleCount: result.bundleCount,
                    eligibleWeight: result.eligibleWeight
                )
            },
            getBundleCount: { roundId in
                let backend = try await dbActor.backend()
                return try backend.getBundleCount(roundId: roundId)
            },
            generateNoteWitnesses: { roundId, bundleIndex, walletDbPath, notes in
                let backend = try await dbActor.backend()
                let sdkNotes = notes.map { $0.toSDK() }
                let witnesses = try backend.generateNoteWitnesses(
                    roundId: roundId,
                    bundleIndex: bundleIndex,
                    walletDbPath: walletDbPath,
                    notes: sdkNotes
                )
                return witnesses.map {
                    WitnessData(
                        noteCommitment: Data($0.noteCommitment),
                        position: $0.position,
                        root: Data($0.root),
                        authPath: $0.authPath.map { Data($0) }
                    )
                }
            },
            verifyWitness: { witness in
                let sdkWitness = VotingWitnessData(
                    noteCommitment: [UInt8](witness.noteCommitment),
                    position: witness.position,
                    root: [UInt8](witness.root),
                    authPath: witness.authPath.map { [UInt8]($0) }
                )
                return try VotingRustBackend.verifyWitness(sdkWitness)
            },
            generateHotkey: { roundId, seed in
                let backend = try await dbActor.backend()
                let hotkey = try backend.generateHotkey(roundId: roundId, seed: seed)
                return VotingModels.VotingHotkey(
                    secretKey: Data(hotkey.secretKey),
                    publicKey: Data(hotkey.publicKey),
                    address: hotkey.address
                )
            },
            // swiftlint:disable:next line_length
            buildGovernancePczt: { roundId, bundleIndex, notes, senderSeed, hotkeySeed, networkId, accountIndex, roundName, orchardFvkOverride, keystoneSeedFingerprintOverride in
                let backend = try await dbActor.backend()
                _ = try backend.generateHotkey(roundId: roundId, seed: hotkeySeed)
                let inputs: VotingDelegationInputs
                let actualFvkBytes: [UInt8]
                if let orchardFvkOverride {
                    guard let keystoneSeedFingerprintOverride else {
                        throw VotingCryptoError.invalidKeystoneMetadata
                    }
                    inputs = try VotingRustBackend.generateDelegationInputsWithFvk(
                        fvkBytes: [UInt8](orchardFvkOverride),
                        hotkeySeed: hotkeySeed,
                        networkId: networkId,
                        accountIndex: accountIndex,
                        seedFingerprint: [UInt8](keystoneSeedFingerprintOverride)
                    )
                    actualFvkBytes = [UInt8](orchardFvkOverride)
                } else {
                    inputs = try VotingRustBackend.generateDelegationInputs(
                        senderSeed: senderSeed,
                        hotkeySeed: hotkeySeed,
                        networkId: networkId,
                        accountIndex: accountIndex
                    )
                    actualFvkBytes = inputs.fvkBytes
                }
                let sdkNotes = notes.map { $0.toSDK() }
                // NU6 consensus branch ID; coin_type 133 = mainnet, 1 = testnet
                let consensusBranchId: UInt32 = 0xC8E7_1055
                let coinType: UInt32 = networkId == 0 ? 133 : 1
                let result = try backend.buildGovernancePczt(
                    roundId: roundId,
                    bundleIndex: bundleIndex,
                    notes: sdkNotes,
                    fvkBytes: actualFvkBytes,
                    hotkeyRawAddress: inputs.hotkeyRawAddress,
                    consensusBranchId: consensusBranchId,
                    coinType: coinType,
                    seedFingerprint: inputs.seedFingerprint,
                    accountIndex: accountIndex,
                    roundName: roundName,
                    addressIndex: 0
                )
                publishState(backend: backend, roundId: roundId)
                return GovernancePcztResult(
                    pcztBytes: Data(result.pcztBytes),
                    rk: Data(result.rk),
                    alpha: Data(result.alpha),
                    nfSigned: Data(result.nfSigned),
                    cmxNew: Data(result.cmxNew),
                    govNullifiers: result.govNullifiers.map { Data($0) },
                    van: Data(result.van),
                    vanCommRand: Data(result.vanCommRand),
                    dummyNullifiers: result.dummyNullifiers.map { Data($0) },
                    rhoSigned: Data(result.rhoSigned),
                    paddedCmx: result.paddedCmx.map { Data($0) },
                    rseedSigned: Data(result.rseedSigned),
                    rseedOutput: Data(result.rseedOutput),
                    actionBytes: Data(result.actionBytes),
                    actionIndex: result.actionIndex
                )
            },
            storeTreeState: { roundId, treeState in
                let backend = try await dbActor.backend()
                try backend.storeTreeState(roundId: roundId, treeStateBytes: [UInt8](treeState))
            },
            extractSpendAuthSignatureFromSignedPczt: { signedPczt, actionIndex in
                Data(try VotingRustBackend.extractSpendAuthSig(
                    signedPcztBytes: [UInt8](signedPczt),
                    actionIndex: actionIndex
                ))
            },
            extractPcztSighash: { pcztBytes in
                Data(try VotingRustBackend.extractPcztSighash(pcztBytes: [UInt8](pcztBytes)))
            },
            // swiftlint:disable:next line_length
            buildAndProveDelegation: { roundId, bundleIndex, bundleNotes, senderSeed, hotkeySeed, networkId, accountIndex, pirServerUrl in
                AsyncThrowingStream { continuation in
                    Task.detached {
                        do {
                            let backend = try await dbActor.backend()
                            let inputs = try VotingRustBackend.generateDelegationInputs(
                                senderSeed: senderSeed,
                                hotkeySeed: hotkeySeed,
                                networkId: networkId,
                                accountIndex: accountIndex
                            )
                            let sdkNotes = bundleNotes.map { $0.toSDK() }
                            let result = try backend.buildAndProveDelegation(
                                roundId: roundId,
                                bundleIndex: bundleIndex,
                                notes: sdkNotes,
                                hotkeyRawAddress: inputs.hotkeyRawAddress,
                                pirServerUrl: pirServerUrl,
                                networkId: networkId,
                                progress: { progress in
                                    continuation.yield(.progress(progress))
                                }
                            )
                            publishState(backend: backend, roundId: roundId)
                            continuation.yield(.completed(Data(result.proof)))
                            continuation.finish()
                        } catch {
                            continuation.finish(throwing: error)
                        }
                    }
                }
            },
            extractOrchardFvkFromUfvk: { ufvkStr, networkId in
                Data(try VotingRustBackend.extractOrchardFvkFromUfvk(ufvkStr: ufvkStr, networkId: networkId))
            },
            decomposeWeight: { weight in
                (try? VotingRustBackend.decomposeWeight(weight)) ?? []
            },
            encryptShares: { roundId, shares in
                let backend = try await dbActor.backend()
                let encrypted = try backend.encryptShares(roundId: roundId, shares: shares)
                return encrypted.map {
                    EncryptedShare(
                        c1: Data($0.c1),
                        c2: Data($0.c2),
                        shareIndex: $0.shareIndex,
                        plaintextValue: $0.plaintextValue,
                        randomness: Data($0.randomness)
                    )
                }
            },
            // swiftlint:disable:next line_length
            buildVoteCommitment: { roundId, bundleIndex, hotkeySeed, networkId, proposalId, choice, numOptions, vanAuthPath, vanPosition, anchorHeight in
                AsyncThrowingStream { continuation in
                    Task.detached {
                        do {
                            let backend = try await dbActor.backend()
                            let result = try backend.buildVoteCommitment(
                                roundId: roundId,
                                bundleIndex: bundleIndex,
                                hotkeySeed: hotkeySeed,
                                networkId: networkId,
                                proposalId: proposalId,
                                choice: choice.ffiValue,
                                numOptions: numOptions,
                                vanAuthPath: vanAuthPath.map { [UInt8]($0) },
                                vanPosition: vanPosition,
                                anchorHeight: anchorHeight,
                                progress: { progress in
                                    continuation.yield(.progress(progress))
                                }
                            )
                            publishState(backend: backend, roundId: roundId)
                            let bundle = VoteCommitmentBundle(
                                vanNullifier: Data(result.vanNullifier),
                                voteAuthorityNoteNew: Data(result.voteAuthorityNoteNew),
                                voteCommitment: Data(result.voteCommitment),
                                proposalId: proposalId,
                                proof: Data(result.proof),
                                encShares: result.encShares.map {
                                    VotingModels.EncryptedShare(
                                        c1: Data($0.c1),
                                        c2: Data($0.c2),
                                        shareIndex: $0.shareIndex,
                                        plaintextValue: $0.plaintextValue,
                                        randomness: Data($0.randomness)
                                    )
                                },
                                anchorHeight: result.anchorHeight,
                                voteRoundId: result.voteRoundId,
                                sharesHash: Data(result.sharesHash),
                                shareBlindFactors: result.shareBlinds.map { Data($0) },
                                shareComms: result.shareComms.map { Data($0) },
                                rVpkBytes: Data(result.rVpkBytes),
                                alphaV: Data(result.alphaV)
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
                let backend = try await dbActor.backend()
                let sdkShares = encShares.map {
                    VotingEncryptedShare(
                        c1: [UInt8]($0.c1),
                        c2: [UInt8]($0.c2),
                        shareIndex: $0.shareIndex,
                        plaintextValue: $0.plaintextValue,
                        randomness: [UInt8]($0.randomness)
                    )
                }
                let sdkCommitment = VotingVoteCommitmentBundle(
                    vanNullifier: [UInt8](commitment.vanNullifier),
                    voteAuthorityNoteNew: [UInt8](commitment.voteAuthorityNoteNew),
                    voteCommitment: [UInt8](commitment.voteCommitment),
                    proposalId: commitment.proposalId,
                    proof: [UInt8](commitment.proof),
                    encShares: sdkShares,
                    anchorHeight: commitment.anchorHeight,
                    voteRoundId: commitment.voteRoundId,
                    sharesHash: [UInt8](commitment.sharesHash),
                    shareBlinds: commitment.shareBlindFactors.map { [UInt8]($0) },
                    shareComms: commitment.shareComms.map { [UInt8]($0) },
                    rVpkBytes: [UInt8](commitment.rVpkBytes),
                    alphaV: [UInt8](commitment.alphaV)
                )
                let payloads = try backend.buildSharePayloads(
                    encShares: sdkShares,
                    commitment: sdkCommitment,
                    voteDecision: voteDecision.ffiValue,
                    numOptions: numOptions,
                    vcTreePosition: vcTreePosition
                )
                return payloads.map {
                    SharePayload(
                        sharesHash: Data($0.sharesHash),
                        proposalId: $0.proposalId,
                        voteDecision: $0.voteDecision,
                        encShare: EncryptedShare(
                            c1: Data($0.encShare.c1),
                            c2: Data($0.encShare.c2),
                            shareIndex: $0.encShare.shareIndex,
                            plaintextValue: $0.encShare.plaintextValue,
                            randomness: Data($0.encShare.randomness)
                        ),
                        treePosition: $0.treePosition,
                        shareComms: $0.shareComms.map { Data($0) },
                        primaryBlind: Data($0.primaryBlind)
                    )
                }
            },
            getDelegationSubmission: { roundId, bundleIndex, senderSeed, networkId, accountIndex in
                let backend = try await dbActor.backend()
                let sub = try backend.getDelegationSubmission(
                    roundId: roundId,
                    bundleIndex: bundleIndex,
                    senderSeed: senderSeed,
                    networkId: networkId,
                    accountIndex: accountIndex
                )
                let voteRoundIdBytes = Data(hexString: sub.voteRoundId)
                return DelegationRegistration(
                    rk: Data(sub.rk),
                    spendAuthSig: Data(sub.spendAuthSig),
                    signedNoteNullifier: Data(sub.nfSigned),
                    cmxNew: Data(sub.cmxNew),
                    vanCmx: Data(sub.govComm),
                    govNullifiers: sub.govNullifiers.map { Data($0) },
                    proof: Data(sub.proof),
                    voteRoundId: voteRoundIdBytes,
                    sighash: Data(sub.sighash)
                )
            },
            getDelegationSubmissionWithKeystoneSig: { roundId, bundleIndex, keystoneSig, keystoneSighash in
                let backend = try await dbActor.backend()
                let sub = try backend.getDelegationSubmissionWithKeystoneSig(
                    roundId: roundId,
                    bundleIndex: bundleIndex,
                    sig: [UInt8](keystoneSig),
                    sighash: [UInt8](keystoneSighash)
                )
                let voteRoundIdBytes = Data(hexString: sub.voteRoundId)
                return DelegationRegistration(
                    rk: Data(sub.rk),
                    spendAuthSig: Data(sub.spendAuthSig),
                    signedNoteNullifier: Data(sub.nfSigned),
                    cmxNew: Data(sub.cmxNew),
                    vanCmx: Data(sub.govComm),
                    govNullifiers: sub.govNullifiers.map { Data($0) },
                    proof: Data(sub.proof),
                    voteRoundId: voteRoundIdBytes,
                    sighash: Data(sub.sighash)
                )
            },
            storeVanPosition: { roundId, bundleIndex, position in
                let backend = try await dbActor.backend()
                try backend.storeVanPosition(roundId: roundId, bundleIndex: bundleIndex, position: position)
            },
            syncVoteTree: { roundId, nodeUrl in
                let backend = try await dbActor.backend()
                return try backend.syncVoteTree(roundId: roundId, nodeUrl: nodeUrl)
            },
            generateVanWitness: { roundId, bundleIndex, anchorHeight in
                let backend = try await dbActor.backend()
                let witness = try backend.generateVanWitness(roundId: roundId, bundleIndex: bundleIndex, anchorHeight: anchorHeight)
                return VanWitness(
                    authPath: witness.authPath.map { Data($0) },
                    position: witness.position,
                    anchorHeight: witness.anchorHeight
                )
            },
            markVoteSubmitted: { roundId, bundleIndex, proposalId in
                let backend = try await dbActor.backend()
                try backend.markVoteSubmitted(roundId: roundId, bundleIndex: bundleIndex, proposalId: proposalId)
                publishState(backend: backend, roundId: roundId)
            },
            resetTreeClient: {
                let backend = try await dbActor.backend()
                try backend.resetTreeClient()
            },
            signCastVote: { hotkeySeed, networkId, bundle in
                let sig = try VotingRustBackend.signCastVote(
                    hotkeySeed: hotkeySeed,
                    networkId: networkId,
                    voteRoundIdHex: bundle.voteRoundId,
                    rVpkBytes: [UInt8](bundle.rVpkBytes),
                    vanNullifier: [UInt8](bundle.vanNullifier),
                    voteAuthorityNoteNew: [UInt8](bundle.voteAuthorityNoteNew),
                    voteCommitment: [UInt8](bundle.voteCommitment),
                    proposalId: bundle.proposalId,
                    anchorHeight: bundle.anchorHeight,
                    alphaV: [UInt8](bundle.alphaV)
                )
                return CastVoteSignature(
                    voteAuthSig: Data(sig.voteAuthSig)
                )
            },
            extractNcRoot: { treeStateBytes in
                Data(try VotingRustBackend.extractNcRoot(treeStateBytes: [UInt8](treeStateBytes)))
            }
        )
    }
}

// MARK: - DatabaseActor

/// Thread-safe holder for the VotingRustBackend instance.
private actor DatabaseActor {
    private var _backend: VotingRustBackend?

    func open(path: String) throws {
        let b = VotingRustBackend()
        try b.open(path: path)
        _backend = b
    }

    func backend() throws -> VotingRustBackend {
        guard let _backend else {
            throw VotingCryptoError.databaseNotOpen
        }
        return _backend
    }
}

// MARK: - Helpers

enum VotingCryptoError: LocalizedError {
    case proofFailed(String)
    case databaseNotOpen
    case hotkeySeedBindingMismatch
    case invalidSpendAuthSignatureLength(Int)
    case invalidKeystoneMetadata

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
        case .invalidKeystoneMetadata:
            return "Missing or invalid Keystone signing metadata."
        }
    }
}

private extension VoteChoice {
    var ffiValue: UInt32 { index }

    static func fromFFI(_ value: UInt32) -> VoteChoice { .option(value) }
}

public extension Data {
    var hexString: String {
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
    func toSDK() -> VotingNoteInfo {
        VotingNoteInfo(
            commitment: [UInt8](commitment),
            nullifier: [UInt8](nullifier),
            value: value,
            position: position,
            diversifier: [UInt8](diversifier),
            rho: [UInt8](rho),
            rseed: [UInt8](rseed),
            scope: scope,
            ufvkStr: ufvkStr
        )
    }
}

private extension VotingRoundPhase {
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

private extension VotingVoteRecord {
    func toModel() -> VotingModels.VoteRecord {
        VotingModels.VoteRecord(
            proposalId: proposalId,
            bundleIndex: bundleIndex,
            choice: VoteChoice.fromFFI(choice),
            submitted: submitted
        )
    }
}
