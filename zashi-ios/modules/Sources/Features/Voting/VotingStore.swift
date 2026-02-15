import Combine
import Foundation
import ComposableArchitecture
import DatabaseFiles
import Generated
import KeystoneHandler
import MnemonicClient
import Models
import Pasteboard
import Scan
import SDKSynchronizer
import UIComponents
import Utils
import VotingAPIClient
import VotingCryptoClient
import VotingModels
import WalletStorage
import ZcashSDKEnvironment
import ZcashLightClientKit

private enum VotingFlowError: LocalizedError {
    case missingActiveSession
    case missingSigningAccount
    case missingHotkeyAddress
    case missingPendingDelegationAction
    case missingPendingUnsignedPczt
    case invalidDelegationSignature
    case missingVoteCommitmentBundle

    var errorDescription: String? {
        switch self {
        case .missingActiveSession:
            return "missing active voting session"
        case .missingSigningAccount:
            return "missing signing account for delegation PCZT"
        case .missingHotkeyAddress:
            return "missing hotkey address for delegation PCZT"
        case .missingPendingDelegationAction:
            return "missing pending delegation action"
        case .missingPendingUnsignedPczt:
            return "missing pending unsigned delegation PCZT"
        case .invalidDelegationSignature:
            return "Keystone signed the PCZT shielded sighash, which does not match the delegation action sighash required by ZKP #1."
        case .missingVoteCommitmentBundle:
            return "vote commitment build completed without a commitment bundle"
        }
    }
}

@Reducer
public struct Voting {
    @Dependency(\.databaseFiles) var databaseFiles
    @Dependency(\.keystoneHandler) var keystoneHandler
    @Dependency(\.mnemonic) var mnemonic
    @Dependency(\.pasteboard) var pasteboard
    @Dependency(\.sdkSynchronizer) var sdkSynchronizer
    @Dependency(\.votingAPI) var votingAPI
    @Dependency(\.votingCrypto) var votingCrypto
    @Dependency(\.walletStorage) var walletStorage
    @Dependency(\.zcashSDKEnvironment) var zcashSDKEnvironment
    @ObservableState
    public struct State: Equatable {
        public enum Screen: Equatable {
            case loading
            case delegationSigning
            case proposalList
            case proposalDetail(id: UInt32)
            case complete
        }

        public struct PendingVote: Equatable {
            public var proposalId: UInt32
            public var choice: VoteChoice
        }

        public struct NoteWitnessResult: Equatable, Identifiable {
            public var id: UInt64 { position }
            public let position: UInt64
            public let value: UInt64
            public let verified: Bool
        }

        public enum WitnessStatus: Equatable {
            case notStarted
            case inProgress
            case completed
            case failed(String)
        }

        public struct WitnessTiming: Equatable {
            public let treeStateFetchMs: UInt64
            public let witnessGenerationMs: UInt64
            public let verificationMs: UInt64
            public var totalMs: UInt64 { treeStateFetchMs + witnessGenerationMs + verificationMs }
        }

        public enum KeystoneSigningStatus: Equatable {
            case idle
            case preparingRequest
            case awaitingSignature
            case parsingSignature
            case failed(String)
        }

        public var screenStack: [Screen] = [.loading]
        public var votingRound: VotingRound
        public var votes: [UInt32: VoteChoice] = [:]
        public var votingWeight: UInt64
        public var isKeystoneUser: Bool
        public var roundId: String
        public var activeSession: VotingSession?

        /// Cached wallet notes from the snapshot query, used by delegation proof.
        public var walletNotes: [NoteInfo] = []

        /// Hotkey address derived from keychain mnemonic, shown on delegation signing screen.
        public var hotkeyAddress: String?

        @Shared(.inMemory(.toast)) public var toast: Toast.Edge? = nil

        public var selectedProposalId: UInt32?

        // Vote awaiting user confirmation in detail view
        public var pendingVote: PendingVote?

        // Witness verification results
        public var noteWitnessResults: [NoteWitnessResult] = []
        public var witnessStatus: WitnessStatus = .notStarted
        /// Cached witness data from verification, used as inclusion proofs for delegation proof.
        public var cachedWitnesses: [WitnessData] = []
        /// Timing breakdown from the last witness generation run.
        public var witnessTiming: WitnessTiming?

        // ZKP #1 (delegation) — runs in background
        public var delegationProofStatus: ProofStatus = .notStarted
        public var keystoneSigningStatus: KeystoneSigningStatus = .idle
        public var pendingDelegationAction: DelegationAction?
        /// Governance PCZT result for Keystone signing flow (contains metadata + pczt_bytes).
        public var pendingGovernancePczt: GovernancePcztResult?
        /// Unsigned delegation PCZT request shown as QR and used for signature extraction.
        public var pendingUnsignedDelegationPczt: Pczt?
        @Presents public var keystoneScan: Scan.State?

        /// Most recent Vote Commitment (VC) bundle built for UI/debug stubs.
        public var lastVoteCommitmentBundle: VoteCommitmentBundle?

        /// Last tx hash returned by submitVoteCommitment, used for completion/debug UI.
        public var lastVoteCommitmentTxHash: String?

        public var currentScreen: Screen {
            screenStack.last ?? .proposalList
        }

        public var votingWeightZECString: String {
            let zec = Double(votingWeight) / 100_000_000.0
            return String(format: "%.2f", zec)
        }

        public var votedCount: Int {
            votes.count
        }

        public var totalProposals: Int {
            votingRound.proposals.count
        }

        public var allVoted: Bool {
            votedCount == totalProposals
        }

        public var isDelegationReady: Bool {
            delegationProofStatus == .complete
        }

        /// Whether the previous vote's VAN has landed in the vote commitment tree.
        /// Always true in the prototype; real implementation checks tree sync.
        public var canConfirmVote: Bool {
            true
        }

        public var nextUnvotedProposalId: UInt32? {
            votingRound.proposals.first { votes[$0.id] == nil }?.id
        }

        public var activeProposalId: UInt32? {
            selectedProposalId ?? nextUnvotedProposalId
        }

        public var selectedProposal: VotingModels.Proposal? {
            if case .proposalDetail(let id) = currentScreen {
                return votingRound.proposals.first { $0.id == id }
            }
            return nil
        }

        // Index of the proposal currently shown in detail
        public var detailProposalIndex: Int? {
            if case .proposalDetail(let id) = currentScreen {
                return votingRound.proposals.firstIndex { $0.id == id }
            }
            return nil
        }

        public init(
            votingRound: VotingRound = MockVotingService.votingRound,
            votingWeight: UInt64 = 0,
            isKeystoneUser: Bool = false,
            roundId: String = ""
        ) {
            self.votingRound = votingRound
            self.votingWeight = votingWeight
            self.isKeystoneUser = isKeystoneUser
            self.roundId = roundId
        }
    }

    let cancelStateStreamId = UUID()

    public enum Action: Equatable {
        // Navigation
        case dismissFlow
        case goBack

        // Initialization (DB, wallet notes, hotkey)
        case initialize
        case activeSessionLoaded(VotingSession)
        case votingWeightLoaded(UInt64, [NoteInfo])
        case initializeFailed(String)
        case hotkeyLoaded(String)

        // DB state stream (single source of truth)
        case votingDbStateChanged(VotingDbState)

        // Witness verification
        case verifyWitnesses
        case rerunWitnessVerification
        case witnessVerificationCompleted([State.NoteWitnessResult], [WitnessData], State.WitnessTiming)
        case witnessVerificationFailed(String)

        // Round resume check (skip delegation screen if already authorized)
        case roundResumeChecked(alreadyAuthorized: Bool)

        // Delegation signing
        case copyHotkeyAddress
        case delegationApproved
        case delegationRejected
        case keystoneSigningPrepared(GovernancePcztResult, Pczt)
        case keystoneSigningFailed(String)
        case openKeystoneSignatureScan
        case retryKeystoneSigning
        case spendAuthSignatureExtracted(Data)
        case spendAuthSignatureExtractionFailed(String)
        case keystoneScan(PresentationAction<Scan.Action>)

        // Background ZKP delegation
        case startDelegationProof
        case delegationProofProgress(Double)
        case delegationProofCompleted
        case delegationProofFailed(String)

        // Proposal list
        case proposalTapped(UInt32)

        // Proposal detail
        case castVote(proposalId: UInt32, choice: VoteChoice)
        case confirmVote
        case cancelPendingVote
        case voteCommitmentBuilt(VoteCommitmentBundle)
        case voteCommitmentSubmitted(String)
        case advanceAfterVote(nextId: UInt32?)
        case backToList
        case nextProposalDetail
        case previousProposalDetail

        // Complete
        case doneTapped
    }

    public init() {}

    public var body: some Reducer<State, Action> {
        Reduce { state, action in
            switch action {
            // MARK: - Navigation

            case .dismissFlow:
                return .cancel(id: cancelStateStreamId)

            case .goBack:
                if state.screenStack.count > 1 {
                    state.screenStack.removeLast()
                }
                return .none

            // MARK: - Initialization

            case .initialize:
                // Non-Keystone users skip the delegation signing screen entirely —
                // set this synchronously so they never see it flash.
                if !state.isKeystoneUser {
                    state.screenStack = [.proposalList]
                }
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                return .run { [votingAPI, votingCrypto, mnemonic, walletStorage] send in
                    // Open the voting database (needed for FFI method)
                    let dbPath = FileManager.default
                        .urls(for: .documentDirectory, in: .userDomainMask)[0]
                        .appendingPathComponent("voting.sqlite3").path
                    try await votingCrypto.openDatabase(dbPath)

                    let activeSession = try await votingAPI.fetchActiveVotingSession()
                    let snapshotHeight = activeSession.snapshotHeight
                    let roundId = activeSession.voteRoundId.hexString
                    await send(.activeSessionLoaded(activeSession))

                    let notes = try await votingCrypto.getWalletNotes(
                        walletDbPath, snapshotHeight, networkId
                    )
                    let totalWeight = notes.reduce(UInt64(0)) { $0 + $1.value }
                    print("[Voting] Loaded \(notes.count) notes at height \(snapshotHeight), total weight: \(totalWeight)")
                    await send(.votingWeightLoaded(totalWeight, notes))

                    // Load or generate voting hotkey mnemonic, derive address for UI
                    do {
                        let phrase: String
                        if let stored = try? walletStorage.exportVotingHotkey() {
                            phrase = stored.seedPhrase.value()
                        } else {
                            phrase = try mnemonic.randomMnemonic()
                            try walletStorage.importVotingHotkey(phrase)
                        }
                        let seed = try mnemonic.toSeed(phrase)
                        let hotkey = try await votingCrypto.generateHotkey(roundId, seed)
                        print("[Voting] Hotkey address: \(hotkey.address)")
                        await send(.hotkeyLoaded(hotkey.address))
                    } catch {
                        print("[Voting] Failed to generate hotkey: \(error)")
                    }
                } catch: { error, send in
                    print("[Voting] Failed to load wallet notes: \(error)")
                    await send(.initializeFailed(error.localizedDescription))
                }

            case .activeSessionLoaded(let session):
                state.activeSession = session
                state.roundId = session.voteRoundId.hexString
                state.votingRound = sessionBackedRound(from: session, fallback: state.votingRound)
                reconcileProposalState(&state)
                return .none

            case .votingWeightLoaded(let weight, let notes):
                state.votingWeight = weight
                state.walletNotes = notes
                return .send(.verifyWitnesses)

            case .initializeFailed(let error):
                print("[Voting] Initialization error: \(error)")
                return .none

            case .hotkeyLoaded(let address):
                state.hotkeyAddress = address
                return .none

            // MARK: - Witness Verification

            case .verifyWitnesses:
                guard let activeSession = state.activeSession else {
                    state.witnessStatus = .failed("missing active session")
                    return .none
                }
                state.witnessStatus = .inProgress
                state.witnessTiming = nil
                let roundId = activeSession.voteRoundId.hexString
                let snapshotHeight = activeSession.snapshotHeight
                let notes = state.walletNotes
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                return .run { [sdkSynchronizer, votingCrypto] send in
                    // Check if this round already exists and is past delegation
                    let existingState = try? await votingCrypto.getRoundState(roundId)
                    let alreadyAuthorized = existingState.map {
                        $0.phase == .delegationProved || $0.phase == .voteReady
                    } ?? false

                    if alreadyAuthorized {
                        await send(.roundResumeChecked(alreadyAuthorized: true))
                        return
                    }

                    // Fresh round — clear and initialize
                    try? await votingCrypto.clearRound(roundId)
                    let params = VotingRoundParams(
                        voteRoundId: activeSession.voteRoundId,
                        snapshotHeight: snapshotHeight,
                        eaPK: activeSession.eaPK,
                        ncRoot: activeSession.ncRoot,
                        nullifierIMTRoot: activeSession.nullifierIMTRoot
                    )
                    try await votingCrypto.initRound(params, nil)

                    // Skip witness pipeline if wallet has no notes at snapshot height
                    guard !notes.isEmpty else {
                        await send(.witnessVerificationCompleted([], [], Voting.State.WitnessTiming(
                            treeStateFetchMs: 0, witnessGenerationMs: 0, verificationMs: 0
                        )))
                        return
                    }

                    // Phase 1: Fetch tree state from lightwalletd
                    let t0 = ContinuousClock.now
                    let treeStateBytes = try await sdkSynchronizer.getTreeState(snapshotHeight)
                    try await votingCrypto.storeTreeState(roundId, treeStateBytes)
                    let t1 = ContinuousClock.now
                    let fetchMs = UInt64(t0.duration(to: t1).components.seconds * 1000)
                        + UInt64(t0.duration(to: t1).components.attoseconds / 1_000_000_000_000_000)
                    print("[Voting] Tree state fetch: \(fetchMs)ms")

                    // Phase 2: Generate witnesses (includes Rust-side verification)
                    let witnesses = try await votingCrypto.generateNoteWitnesses(roundId, walletDbPath, notes)
                    let t2 = ContinuousClock.now
                    let genMs = UInt64(t1.duration(to: t2).components.seconds * 1000)
                        + UInt64(t1.duration(to: t2).components.attoseconds / 1_000_000_000_000_000)
                    print("[Voting] Witness generation: \(genMs)ms (\(witnesses.count) notes)")

                    // Phase 3: Verify each witness on Swift side for UI display
                    var results: [Voting.State.NoteWitnessResult] = []
                    for (i, witness) in witnesses.enumerated() {
                        let verified = (try? await votingCrypto.verifyWitness(witness)) ?? false
                        let note = notes[i]
                        results.append(.init(position: note.position, value: note.value, verified: verified))
                        print("[Voting] Note pos=\(note.position) value=\(note.value) verified=\(verified)")
                    }
                    let t3 = ContinuousClock.now
                    let verifyMs = UInt64(t2.duration(to: t3).components.seconds * 1000)
                        + UInt64(t2.duration(to: t3).components.attoseconds / 1_000_000_000_000_000)
                    print("[Voting] Swift verification: \(verifyMs)ms")
                    print("[Voting] Total witness pipeline: \(fetchMs + genMs + verifyMs)ms")

                    let timing = Voting.State.WitnessTiming(
                        treeStateFetchMs: fetchMs,
                        witnessGenerationMs: genMs,
                        verificationMs: verifyMs
                    )
                    await send(.witnessVerificationCompleted(results, witnesses, timing))
                } catch: { error, send in
                    print("[Voting] Witness verification failed: \(error)")
                    await send(.witnessVerificationFailed(error.localizedDescription))
                }

            case .rerunWitnessVerification:
                // Invalidate cached witnesses and re-run from scratch
                state.noteWitnessResults = []
                state.cachedWitnesses = []
                state.witnessTiming = nil
                return .send(.verifyWitnesses)

            case .witnessVerificationCompleted(let results, let witnesses, let timing):
                state.noteWitnessResults = results
                state.cachedWitnesses = witnesses
                state.witnessTiming = timing
                state.witnessStatus = .completed
                // Non-Keystone users skip the delegation signing screen entirely
                if !state.isKeystoneUser {
                    state.screenStack = [.proposalList]
                    state.delegationProofStatus = .complete
                    return .publisher {
                        votingCrypto.stateStream()
                            .receive(on: DispatchQueue.main)
                            .map(Action.votingDbStateChanged)
                    }
                    .cancellable(id: cancelStateStreamId, cancelInFlight: true)
                }
                // Keystone fresh round: now show the delegation signing screen
                state.screenStack = [.delegationSigning]
                return .none

            case .witnessVerificationFailed(let error):
                state.witnessStatus = .failed(error)
                return .none

            // MARK: - Round Resume

            case .roundResumeChecked(let alreadyAuthorized):
                if alreadyAuthorized {
                    state.delegationProofStatus = .complete
                    state.screenStack = [.proposalList]
                    state.witnessStatus = .completed
                    // Start state stream to sync votes and hotkey from the existing round
                    return .publisher {
                        votingCrypto.stateStream()
                            .receive(on: DispatchQueue.main)
                            .map(Action.votingDbStateChanged)
                    }
                    .cancellable(id: cancelStateStreamId, cancelInFlight: true)
                }
                return .none

            // MARK: - DB State Stream

            case .votingDbStateChanged(let dbState):
                // Votes: DB is source of truth, overwrite in-memory dict
                state.votes = dbState.votesByProposal
                // Proof status: if DB says proof succeeded and we're not actively generating, sync it
                if dbState.roundState.proofGenerated && state.delegationProofStatus != .complete {
                    state.delegationProofStatus = .complete
                }
                // Sync hotkey address from DB if available
                if let addr = dbState.roundState.hotkeyAddress {
                    state.hotkeyAddress = addr
                }
                print("[Voting] DB state: phase=\(dbState.roundState.phase), \(dbState.votes.count) votes")
                return .none

            // MARK: - Delegation Signing

            case .copyHotkeyAddress:
                if let address = state.hotkeyAddress {
                    pasteboard.setString(address.redacted)
                    state.$toast.withLock { $0 = .top(L10n.General.copiedToTheClipboard) }
                }
                return .none

            case .delegationApproved:
                if !state.isKeystoneUser {
                    state.screenStack = [.proposalList]
                    // Direct-wallet voting does not require delegation proof generation.
                    state.delegationProofStatus = .complete
                    return .none
                }
                return .send(.startDelegationProof)

            case .delegationRejected:
                state.pendingDelegationAction = nil
                state.pendingGovernancePczt = nil
                state.pendingUnsignedDelegationPczt = nil
                state.keystoneSigningStatus = .idle
                return .send(.dismissFlow)

            case .retryKeystoneSigning:
                state.pendingDelegationAction = nil
                state.pendingGovernancePczt = nil
                state.pendingUnsignedDelegationPczt = nil
                state.keystoneSigningStatus = .idle
                return .send(.startDelegationProof)

            // MARK: - Background ZKP Delegation

            case .startDelegationProof:
                guard let activeSession = state.activeSession else {
                    return .send(.delegationProofFailed(
                        VotingFlowError.missingActiveSession.localizedDescription
                    ))
                }
                if state.isKeystoneUser {
                    state.keystoneSigningStatus = .preparingRequest
                } else {
                    state.delegationProofStatus = .generating(progress: 0)
                }
                let roundId = activeSession.voteRoundId.hexString
                let cachedNotes = state.walletNotes
                let networkId: UInt32 = zcashSDKEnvironment.network.networkType == .mainnet ? 0 : 1
                let accountIndex: UInt32 = 0
                let isKeystoneUser = state.isKeystoneUser
                let roundName = state.votingRound.title
                // Flatten each witness auth path into a single Data for the delegation circuit
                let inclusionProofs = state.cachedWitnesses.map { witness in
                    witness.authPath.reduce(Data()) { $0 + $1 }
                }
                return .merge(
                    // Subscribe to DB state stream (follows SDKSynchronizer pattern)
                    .publisher {
                        votingCrypto.stateStream()
                            .receive(on: DispatchQueue.main)
                            .map(Action.votingDbStateChanged)
                    }
                    .cancellable(id: cancelStateStreamId, cancelInFlight: true),
                    // Run delegation proof pipeline
                    // Round is already initialized and witnesses cached by verifyWitnesses
                    .run { [sdkSynchronizer, votingCrypto, mnemonic, walletStorage] send in
                        // Reload hotkey from keychain (generated during initialize)
                        let senderPhrase = try walletStorage.exportWallet().seedPhrase.value()
                        let senderSeed = try mnemonic.toSeed(senderPhrase)
                        let hotkeyPhrase = try walletStorage.exportVotingHotkey().seedPhrase.value()
                        let hotkeySeed = try mnemonic.toSeed(hotkeyPhrase)
                        if isKeystoneUser {
                            // Build governance PCZT — its single Orchard action IS the governance
                            // dummy action, so Keystone's SpendAuth signature will verify against
                            // the PCZT's ZIP-244 sighash (which binds to the governance values).
                            let govPczt = try await votingCrypto.buildGovernancePczt(
                                roundId,
                                cachedNotes,
                                senderSeed,
                                hotkeySeed,
                                networkId,
                                accountIndex,
                                roundName
                            )
                            let redactedPczt = try await sdkSynchronizer
                                .redactPCZTForSigner(govPczt.pcztBytes)
                            await send(.keystoneSigningPrepared(govPczt, redactedPczt))
                            return
                        }

                        // Non-Keystone path: build delegation action with placeholder signature
                        let action = try await votingCrypto.buildDelegationSignAction(
                            roundId,
                            cachedNotes,
                            senderSeed,
                            hotkeySeed,
                            networkId,
                            accountIndex
                        )
                        let spendAuthSig = action.spendAuthSig ?? Data(repeating: 0x00, count: 64)
                        // Use real Merkle inclusion proofs from verified witnesses
                        _ = try await votingCrypto.buildDelegationWitness(
                            roundId,
                            action,
                            spendAuthSig,
                            inclusionProofs,
                            [Data(repeating: 0x22, count: 32)] // exclusion proofs still mocked
                        )

                        // Generate delegation proof (long-running, reports progress)
                        for try await event in votingCrypto.generateDelegationProof(roundId) {
                            switch event {
                            case .progress(let p):
                                await send(.delegationProofProgress(p))
                            case .completed:
                                await send(.delegationProofCompleted)
                            }
                        }
                    } catch: { error, send in
                        if isKeystoneUser {
                            await send(.keystoneSigningFailed(error.localizedDescription))
                        } else {
                            await send(.delegationProofFailed(error.localizedDescription))
                        }
                    }
                )

            case .keystoneSigningPrepared(let govPczt, let unsignedPczt):
                state.pendingGovernancePczt = govPczt
                state.pendingDelegationAction = govPczt.toDelegationAction()
                state.pendingUnsignedDelegationPczt = unsignedPczt
                state.keystoneSigningStatus = .awaitingSignature
                return .none

            case .keystoneSigningFailed(let error):
                state.keystoneSigningStatus = .failed(error)
                return .none

            case .openKeystoneSignatureScan:
                keystoneHandler.resetQRDecoder()
                var scanState = Scan.State.initial
                scanState.instructions = "Scan signed delegation QR from Keystone"
                scanState.checkers = [.keystoneVotingDelegationPCZTScanChecker]
                state.keystoneScan = scanState
                return .none

            case .keystoneScan(.presented(.foundVotingDelegationPCZT(let signedPczt))):
                state.keystoneScan = nil
                state.keystoneSigningStatus = .parsingSignature
                guard let govPczt = state.pendingGovernancePczt else {
                    return .send(.spendAuthSignatureExtractionFailed(
                        VotingFlowError.missingPendingUnsignedPczt.localizedDescription
                    ))
                }
                let actionIndex = govPczt.actionIndex
                return .run { [votingCrypto] send in
                    let spendAuthSig = try votingCrypto.extractSpendAuthSignatureFromSignedPczt(
                        signedPczt,
                        actionIndex
                    )
                    await send(.spendAuthSignatureExtracted(spendAuthSig))
                } catch: { error, send in
                    await send(.spendAuthSignatureExtractionFailed(error.localizedDescription))
                }

            case .keystoneScan(.presented(.cancelTapped)),
                    .keystoneScan(.dismiss):
                state.keystoneScan = nil
                return .none

            case .keystoneScan:
                return .none

            case .spendAuthSignatureExtracted(let spendAuthSig):
                guard let activeSession = state.activeSession else {
                    return .send(.delegationProofFailed(
                        VotingFlowError.missingActiveSession.localizedDescription
                    ))
                }
                guard let pendingAction = state.pendingDelegationAction else {
                    return .send(.delegationProofFailed(
                        VotingFlowError.missingPendingDelegationAction.localizedDescription
                    ))
                }
                state.pendingDelegationAction = nil
                state.pendingGovernancePczt = nil
                state.pendingUnsignedDelegationPczt = nil
                state.keystoneSigningStatus = .idle
                state.screenStack = [.proposalList]
                state.delegationProofStatus = .generating(progress: 0)

                let roundId = activeSession.voteRoundId.hexString
                let inclusionProofs = state.cachedWitnesses.map { witness in
                    witness.authPath.reduce(Data()) { $0 + $1 }
                }
                return .run { [votingCrypto] send in
                    _ = try await votingCrypto.buildDelegationWitness(
                        roundId,
                        pendingAction,
                        spendAuthSig,
                        inclusionProofs,
                        [Data(repeating: 0x22, count: 32)]
                    )

                    for try await event in votingCrypto.generateDelegationProof(roundId) {
                        switch event {
                        case .progress(let p):
                            await send(.delegationProofProgress(p))
                        case .completed:
                            await send(.delegationProofCompleted)
                        }
                    }
                } catch: { error, send in
                    await send(.delegationProofFailed(error.localizedDescription))
                }

            case .spendAuthSignatureExtractionFailed(let error):
                state.keystoneSigningStatus = .failed(error)
                return .none

            case .delegationProofProgress(let progress):
                state.delegationProofStatus = .generating(progress: progress)
                return .none

            case .delegationProofCompleted:
                state.delegationProofStatus = .complete
                return .none

            case .delegationProofFailed(let error):
                state.delegationProofStatus = .failed(error)
                return .none

            // MARK: - Proposal List

            case .proposalTapped(let id):
                state.selectedProposalId = id
                state.screenStack.append(.proposalDetail(id: id))
                return .none

            // MARK: - Proposal Detail

            case .castVote(let proposalId, let choice):
                // If already confirmed for this proposal, ignore
                guard state.votes[proposalId] == nil else { return .none }
                state.pendingVote = .init(proposalId: proposalId, choice: choice)
                return .none

            case .cancelPendingVote:
                state.pendingVote = nil
                return .none

            case .confirmVote:
                guard let pending = state.pendingVote else { return .none }
                guard let activeSession = state.activeSession else { return .none }
                state.votes[pending.proposalId] = pending.choice
                state.pendingVote = nil

                let proposalId = pending.proposalId
                let choice = pending.choice
                let roundId = state.roundId
                let voteRoundId = activeSession.voteRoundId
                // Placeholder until tree-anchor query is wired in Gov Steps Phase 3 (voting).
                let voteCommTreeAnchorHeight: UInt64 = 0
                let votingWeight = state.votingWeight
                let nextId = nextUnvotedId(after: proposalId, in: state)
                state.lastVoteCommitmentTxHash = nil

                return .merge(
                    // Submit this vote to chain in background
                    .run { [votingAPI, votingCrypto] send in
                        // Decompose weight into binary shares, encrypt under EA public key
                        let shares = votingCrypto.decomposeWeight(votingWeight)
                        print("[Voting] decomposeWeight(\(votingWeight)) → \(shares.count) shares")
                        let encShares = try await votingCrypto.encryptShares(roundId, shares)

                        let vanWitness = Data(repeating: 0xDD, count: 64) // mock VAN witness

                        // Build vote commitment + ZKP #2 (stored in DB)
                        var builtBundle: VoteCommitmentBundle?
                        for try await event in votingCrypto.buildVoteCommitment(roundId, proposalId, choice, encShares, vanWitness) {
                            if case .completed(let bundle) = event {
                                builtBundle = bundle
                                await send(.voteCommitmentBuilt(bundle))
                            }
                        }
                        guard let builtBundle else {
                            throw VotingFlowError.missingVoteCommitmentBundle
                        }

                        // Submit to chain (API stubs for now)
                        let bundle = VoteCommitmentBundle(
                            vanNullifier: builtBundle.vanNullifier,
                            voteAuthorityNoteNew: builtBundle.voteAuthorityNoteNew,
                            voteCommitment: builtBundle.voteCommitment,
                            proposalId: proposalId,
                            proof: builtBundle.proof,
                            voteRoundId: voteRoundId,
                            voteCommTreeAnchorHeight: voteCommTreeAnchorHeight
                        )
                        let txResult = try await votingAPI.submitVoteCommitment(bundle)
                        await send(.voteCommitmentSubmitted(txResult.txHash))
                        let payloads = try await votingCrypto.buildSharePayloads(encShares, bundle)
                        try await votingAPI.delegateShares(payloads)

                        // Mark vote submitted in DB
                        try await votingCrypto.markVoteSubmitted(roundId, proposalId)
                    } catch: { error, _ in
                        print("[Voting] vote submission failed: \(error)")
                    },
                    // Advance UI after brief pause
                    .run { send in
                        try await Task.sleep(for: .milliseconds(600))
                        await send(.advanceAfterVote(nextId: nextId))
                    }
                )

            case .voteCommitmentBuilt(let bundle):
                state.lastVoteCommitmentBundle = bundle
                return .none

            case .voteCommitmentSubmitted(let txHash):
                state.lastVoteCommitmentTxHash = txHash
                return .none

            case .advanceAfterVote(let nextId):
                if case .proposalDetail = state.currentScreen {
                    if let nextId {
                        state.selectedProposalId = nextId
                        state.screenStack.removeLast()
                        state.screenStack.append(.proposalDetail(id: nextId))
                    } else {
                        // All proposals voted — go to completion
                        state.screenStack = [.complete]
                    }
                }
                return .none

            case .backToList:
                state.pendingVote = nil
                if case .proposalDetail = state.currentScreen {
                    state.screenStack.removeLast()
                }
                return .none

            case .nextProposalDetail:
                state.pendingVote = nil
                if let index = state.detailProposalIndex,
                   index + 1 < state.votingRound.proposals.count {
                    let nextId = state.votingRound.proposals[index + 1].id
                    state.selectedProposalId = nextId
                    state.screenStack.removeLast()
                    state.screenStack.append(.proposalDetail(id: nextId))
                }
                return .none

            case .previousProposalDetail:
                state.pendingVote = nil
                if let index = state.detailProposalIndex, index > 0 {
                    let prevId = state.votingRound.proposals[index - 1].id
                    state.selectedProposalId = prevId
                    state.screenStack.removeLast()
                    state.screenStack.append(.proposalDetail(id: prevId))
                }
                return .none

            // MARK: - Complete

            case .doneTapped:
                return .send(.dismissFlow)
            }
        }
        .ifLet(\.$keystoneScan, action: \.keystoneScan) {
            Scan()
        }
    }

    // Find the next unvoted proposal after the given one (wrapping around)
    private func nextUnvotedId(after proposalId: UInt32, in state: State) -> UInt32? {
        let proposals = state.votingRound.proposals
        guard let currentIndex = proposals.firstIndex(where: { $0.id == proposalId }) else { return nil }

        // Look forward first, then wrap
        return proposals[(currentIndex + 1)...].first { state.votes[$0.id] == nil }?.id
            ?? proposals[..<currentIndex].first { state.votes[$0.id] == nil }?.id
    }

    private func sessionBackedRound(from session: VotingSession, fallback: VotingRound) -> VotingRound {
        let proposals = session.proposals.isEmpty ? fallback.proposals : session.proposals
        return VotingRound(
            id: session.voteRoundId.hexString,
            title: fallback.title,
            description: fallback.description,
            snapshotHeight: session.snapshotHeight,
            snapshotDate: fallback.snapshotDate,
            votingStart: fallback.votingStart,
            votingEnd: session.voteEndTime,
            proposals: proposals
        )
    }

    private func reconcileProposalState(_ state: inout State) {
        let validProposalIDs = Set(state.votingRound.proposals.map(\.id))
        state.votes = state.votes.filter { validProposalIDs.contains($0.key) }

        if let selectedProposalId = state.selectedProposalId,
           !validProposalIDs.contains(selectedProposalId) {
            state.selectedProposalId = nil
        }

        if let pendingVote = state.pendingVote,
           !validProposalIDs.contains(pendingVote.proposalId) {
            state.pendingVote = nil
        }

        if case .proposalDetail(let proposalId) = state.currentScreen,
           !validProposalIDs.contains(proposalId) {
            if !state.screenStack.isEmpty {
                state.screenStack.removeLast()
            }
            state.screenStack.append(.proposalList)
        }
    }
}
