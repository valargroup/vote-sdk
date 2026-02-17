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

/// Nullifier IMT server used for ZKP #1 exclusion proofs.
private let imtServerBaseUrl = "http://46.101.255.48:3000"

private enum VotingFlowError: LocalizedError {
    case missingActiveSession
    case missingSigningAccount
    case missingHotkeyAddress
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
        case .missingPendingUnsignedPczt:
            return "missing pending unsigned delegation PCZT"
        case .invalidDelegationSignature:
            return "Keystone signed the PCZT shielded sighash, which does not match the delegation action sighash required by ZKP #1."
        case .missingVoteCommitmentBundle:
            return "vote commitment build completed without a commitment bundle"
        }
    }
}

private enum ZallyTestRoundError: LocalizedError {
    case invalidIMTResponse

    var errorDescription: String? {
        switch self {
        case .invalidIMTResponse:
            return "Invalid response from IMT server /root endpoint"
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

        /// Governance PCZT result for Keystone signing flow (contains metadata + pczt_bytes).
        public var pendingGovernancePczt: GovernancePcztResult?
        /// Unsigned delegation PCZT request shown as QR and used for signature extraction.
        public var pendingUnsignedDelegationPczt: Pczt?
        @Presents public var keystoneScan: Scan.State?

        /// Most recent Vote Commitment (VC) bundle built for UI/debug stubs.
        public var lastVoteCommitmentBundle: VoteCommitmentBundle?

        /// Last tx hash returned by submitVoteCommitment, used for completion/debug UI.
        public var lastVoteCommitmentTxHash: String?

        /// Whether a vote commitment is being built and submitted to chain.
        public var isSubmittingVote: Bool = false
        /// Error from the last vote submission attempt.
        public var voteSubmissionError: String?

        /// Whether a test round creation is in progress.
        public var isCreatingTestRound: Bool = false
        /// Error from the last test round creation attempt.
        public var testRoundError: String?

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

        /// Whether the user can confirm a vote: delegation proof must be complete
        /// and no other vote can be in-flight (each vote needs the previous VAN committed).
        public var canConfirmVote: Bool {
            isDelegationReady && !isSubmittingVote
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
        case voteSubmissionFailed(proposalId: UInt32, error: String)
        case advanceAfterVote(nextId: UInt32?)
        case backToList
        case nextProposalDetail
        case previousProposalDetail

        // Test round creation (dev only)
        case createTestRound
        case testRoundCreated
        case testRoundFailed(String)

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
                    return .merge(
                        .publisher {
                            votingCrypto.stateStream()
                                .receive(on: DispatchQueue.main)
                                .map(Action.votingDbStateChanged)
                        }
                        .cancellable(id: cancelStateStreamId, cancelInFlight: true),
                        .send(.startDelegationProof)
                    )
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
                    return .send(.startDelegationProof)
                }
                return .send(.startDelegationProof)

            case .delegationRejected:

                state.pendingGovernancePczt = nil
                state.pendingUnsignedDelegationPczt = nil
                state.keystoneSigningStatus = .idle
                return .send(.dismissFlow)

            case .retryKeystoneSigning:

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
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                let accountIndex: UInt32 = 0
                let isKeystoneUser = state.isKeystoneUser
                let roundName = state.votingRound.title
                // TODO: Source from VotingSession or server config once the nullifier-ingest
                // service endpoint is deployed.
                let imtServerUrl = imtServerBaseUrl
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
                    .run { [sdkSynchronizer, votingCrypto, votingAPI, mnemonic, walletStorage] send in
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

                        // Non-Keystone path: build and prove delegation in one step
                        for try await event in votingCrypto.buildAndProveDelegation(
                            roundId, walletDbPath, senderSeed, hotkeySeed,
                            networkId, accountIndex, imtServerUrl
                        ) {
                            switch event {
                            case .progress(let p):
                                print("[Voting] ZKP #1 progress: \(Int(p * 100))%")
                                await send(.delegationProofProgress(p))
                            case .completed(let proof):
                                print("[Voting] ZKP #1 COMPLETE — proof size: \(proof.count) bytes (real=5216, mock=32)")
                            }
                        }

                        // After proof completes, submit delegation TX to chain
                        let registration = try await votingCrypto.getDelegationSubmission(
                            roundId, senderSeed, networkId, accountIndex
                        )
                        let preTree = try await votingAPI.fetchLatestCommitmentTree()
                        let delegTxResult = try await votingAPI.submitDelegation(registration)
                        print("[Voting] Delegation TX submitted: \(delegTxResult.txHash)")

                        // Poll until the delegation TX lands and the tree grows
                        let postTree = try await votingAPI.awaitCommitmentTreeGrowth(preTree.nextIndex, 30)
                        let vanPosition = UInt32(postTree.nextIndex) - 1
                        try await votingCrypto.storeVanPosition(roundId, vanPosition)
                        print("[Voting] VAN position stored: \(vanPosition)")

                        await send(.delegationProofCompleted)
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

            case .spendAuthSignatureExtracted:
                // TODO: Store spendAuthSig in DB for on-chain submission.
                // The sig is extracted from the Keystone-signed PCZT but not yet
                // persisted — it will be needed when the cosmos submission flow is built.
                guard let activeSession = state.activeSession else {
                    return .send(.delegationProofFailed(
                        VotingFlowError.missingActiveSession.localizedDescription
                    ))
                }

                state.pendingGovernancePczt = nil
                state.pendingUnsignedDelegationPczt = nil
                state.keystoneSigningStatus = .idle
                state.screenStack = [.proposalList]
                state.delegationProofStatus = .generating(progress: 0)

                let roundId = activeSession.voteRoundId.hexString
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                let accountIndex: UInt32 = 0
                // TODO: Source from VotingSession or server config once the nullifier-ingest
                // service endpoint is deployed. For now, the Rust side fetches IMT proofs from
                // this URL for each note's nullifier.
                let imtServerUrl = imtServerBaseUrl
                return .run { [votingCrypto, votingAPI, mnemonic, walletStorage] send in
                    let senderPhrase = try walletStorage.exportWallet().seedPhrase.value()
                    let senderSeed = try mnemonic.toSeed(senderPhrase)
                    let hotkeyPhrase = try walletStorage.exportVotingHotkey().seedPhrase.value()
                    let hotkeySeed = try mnemonic.toSeed(hotkeyPhrase)

                    for try await event in votingCrypto.buildAndProveDelegation(
                        roundId, walletDbPath, senderSeed, hotkeySeed,
                        networkId, accountIndex, imtServerUrl
                    ) {
                        switch event {
                        case .progress(let p):
                            print("[Voting] ZKP #1 progress: \(Int(p * 100))%")
                            await send(.delegationProofProgress(p))
                        case .completed(let proof):
                            print("[Voting] ZKP #1 COMPLETE — proof size: \(proof.count) bytes (real=5216, mock=32)")
                        }
                    }

                    // After proof completes, submit delegation TX to chain
                    let registration = try await votingCrypto.getDelegationSubmission(
                        roundId, senderSeed, networkId, accountIndex
                    )
                    let preTree = try await votingAPI.fetchLatestCommitmentTree()
                    let delegTxResult = try await votingAPI.submitDelegation(registration)
                    print("[Voting] Delegation TX submitted: \(delegTxResult.txHash)")

                    // Poll until the delegation TX lands and the tree grows
                    let postTree = try await votingAPI.awaitCommitmentTreeGrowth(preTree.nextIndex, 30)
                    let vanPosition = UInt32(postTree.nextIndex) - 1
                    try await votingCrypto.storeVanPosition(roundId, vanPosition)
                    print("[Voting] VAN position stored: \(vanPosition)")

                    await send(.delegationProofCompleted)
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
                let userMessage: String
                if error.contains("total_weight must yield at least 1 ballot") {
                    let weightStr = Zatoshi(Int64(state.votingWeight)).decimalString()
                    let requiredStr = Zatoshi(12_500_000).decimalString()
                    userMessage = "Your shielded balance at the snapshot (\(weightStr) ZEC) is below the minimum required to vote (\(requiredStr) ZEC)."
                } else {
                    userMessage = error
                }
                state.delegationProofStatus = .failed(userMessage)
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
                guard state.activeSession != nil else { return .none }
                state.votes[pending.proposalId] = pending.choice
                state.pendingVote = nil
                state.isSubmittingVote = true
                state.voteSubmissionError = nil
                state.lastVoteCommitmentTxHash = nil

                let proposalId = pending.proposalId
                let choice = pending.choice
                let roundId = state.roundId
                let network = zcashSDKEnvironment.network
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                let nextId = nextUnvotedId(after: proposalId, in: state)

                return .run { [votingAPI, votingCrypto, mnemonic, walletStorage] send in
                    // Derive hotkey seed (same seed used during delegation)
                    let hotkeyPhrase = try walletStorage.exportVotingHotkey().seedPhrase.value()
                    let hotkeySeed = try mnemonic.toSeed(hotkeyPhrase)

                    // Sync vote commitment tree from chain and generate VAN witness.
                    // Requires storeVanPosition to have been called after delegation TX.
                    let chainNodeUrl = ZallyAPIConfig.baseURL
                    let anchorHeight = try await votingCrypto.syncVoteTree(roundId, chainNodeUrl)
                    let vanWitness = try await votingCrypto.generateVanWitness(roundId, anchorHeight)
                    print("[Voting] VAN witness: position=\(vanWitness.position), anchor=\(vanWitness.anchorHeight)")

                    // Build vote commitment + ZKP #2 (stored in DB).
                    // The builder internally decomposes weight, encrypts shares under EA pk,
                    // and returns encrypted shares in the bundle.
                    var builtBundle: VoteCommitmentBundle?
                    for try await event in votingCrypto.buildVoteCommitment(
                        roundId, hotkeySeed, networkId, proposalId, choice,
                        vanWitness.authPath, vanWitness.position, vanWitness.anchorHeight
                    ) {
                        if case .completed(let bundle) = event {
                            builtBundle = bundle
                            await send(.voteCommitmentBuilt(bundle))
                        }
                    }
                    guard let builtBundle else {
                        throw VotingFlowError.missingVoteCommitmentBundle
                    }

                    // Sign the cast-vote TX (sighash + spend auth signature)
                    let castVoteSig = try await votingCrypto.signCastVote(
                        hotkeySeed, networkId, builtBundle
                    )

                    // Submit cast-vote TX to chain, polling for tree growth
                    let preVCTree = try await votingAPI.fetchLatestCommitmentTree()
                    let txResult = try await votingAPI.submitVoteCommitment(builtBundle, castVoteSig)
                    await send(.voteCommitmentSubmitted(txResult.txHash))

                    // Wait for the cast-vote TX to land and read the new tree position
                    let postVCTree = try await votingAPI.awaitCommitmentTreeGrowth(preVCTree.nextIndex, 30)
                    let vcTreePosition = postVCTree.nextIndex - 1
                    let payloads = try await votingCrypto.buildSharePayloads(
                        builtBundle.encShares, builtBundle, choice, vcTreePosition
                    )
                    try await votingAPI.delegateShares(payloads, roundId)

                    // Mark vote submitted in DB
                    try await votingCrypto.markVoteSubmitted(roundId, proposalId)

                    // Vote is on chain — advance to next proposal
                    await send(.advanceAfterVote(nextId: nextId))
                } catch: { error, send in
                    print("[Voting] vote submission failed: \(error)")
                    await send(.voteSubmissionFailed(proposalId: proposalId, error: error.localizedDescription))
                }

            case .voteCommitmentBuilt(let bundle):
                state.lastVoteCommitmentBundle = bundle
                return .none

            case .voteCommitmentSubmitted(let txHash):
                state.lastVoteCommitmentTxHash = txHash
                return .none

            case .voteSubmissionFailed(let proposalId, let error):
                state.isSubmittingVote = false
                state.voteSubmissionError = error
                // Remove the optimistic vote since it didn't land on chain
                state.votes.removeValue(forKey: proposalId)
                return .none

            case .advanceAfterVote(let nextId):
                state.isSubmittingVote = false
                state.voteSubmissionError = nil
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

            // MARK: - Test Round Creation

            case .createTestRound:
                state.isCreatingTestRound = true
                state.testRoundError = nil
                let snapshotHeight: UInt64 = 3_235_470
                return .run { [sdkSynchronizer, votingCrypto, votingAPI] send in
                    // 1. Fetch tree state at snapshot height from lightwalletd
                    let treeStateBytes = try await sdkSynchronizer.getTreeState(snapshotHeight)

                    // 2. Extract nc_root from tree state
                    let ncRoot = try votingCrypto.extractNcRoot(treeStateBytes)
                    print("[Voting] nc_root: \(ncRoot.hexString)")

                    // 3. Fetch nullifier_imt_root from IMT server
                    guard let imtURL = URL(string: "\(imtServerBaseUrl)/root") else {
                        throw VotingFlowError.missingActiveSession
                    }
                    let (imtData, _) = try await URLSession.shared.data(from: imtURL)
                    guard let imtJson = try JSONSerialization.jsonObject(with: imtData) as? [String: Any],
                          let rootHex = imtJson["root"] as? String else {
                        throw ZallyTestRoundError.invalidIMTResponse
                    }
                    let nullifierImtRoot = Data(hexString: rootHex)
                    print("[Voting] nullifier_imt_root: \(nullifierImtRoot.hexString)")

                    // 4. Build session payload (ea_pk omitted — the chain auto-fills it)
                    let voteEndTime = Int(Date().addingTimeInterval(5 * 24 * 3600).timeIntervalSince1970)
                    let proposals: [[String: Any]] = MockVotingService.proposals.map { p in
                        [
                            "id": p.id,
                            "title": p.title,
                            "description": p.description
                        ]
                    }

                    let payload: [String: Any] = [
                        "creator": "zvote1admin",
                        "snapshot_height": snapshotHeight,
                        "snapshot_blockhash": Data(repeating: 0xAA, count: 32).base64EncodedString(),
                        "proposals_hash": Data(repeating: 0xBB, count: 32).base64EncodedString(),
                        "vote_end_time": voteEndTime,
                        "nc_root": ncRoot.base64EncodedString(),
                        "nullifier_imt_root": nullifierImtRoot.base64EncodedString(),
                        "vk_zkp1": Data(repeating: 0xF1, count: 64).base64EncodedString(),
                        "vk_zkp2": Data(repeating: 0xF2, count: 64).base64EncodedString(),
                        "vk_zkp3": Data(repeating: 0xF3, count: 64).base64EncodedString(),
                        "proposals": proposals
                    ]

                    // 5. POST to chain
                    try await votingAPI.createTestSession(payload)
                    print("[Voting] Test session created successfully")

                    // 6. Poll until the new round is queryable (TX needs to land in a block)
                    let deadline = Date().addingTimeInterval(30)
                    while Date() < deadline {
                        if let _ = try? await votingAPI.fetchActiveVotingSession() {
                            break
                        }
                        try await Task.sleep(for: .seconds(1))
                    }

                    await send(.testRoundCreated)
                } catch: { error, send in
                    print("[Voting] Test round creation failed: \(error)")
                    await send(.testRoundFailed(error.localizedDescription))
                }

            case .testRoundCreated:
                state.isCreatingTestRound = false
                state.testRoundError = nil
                // Re-initialize the entire flow — new session should now be active
                return .send(.initialize)

            case .testRoundFailed(let error):
                state.isCreatingTestRound = false
                state.testRoundError = error
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
