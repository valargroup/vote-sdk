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
            case noRounds
            case roundsList
            case delegationSigning
            case proposalList
            case proposalDetail(id: UInt32)
            case complete
            case ineligible
            case tallying
            case results
            case error(String)
            case walletSyncing
        }

        public struct RoundListItem: Equatable, Identifiable {
            public var id: String { session.voteRoundId.hexString }
            public let roundNumber: Int
            public let session: VotingSession
            public var title: String {
                session.title.isEmpty ? "Round \(roundNumber)" : session.title
            }
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

        public enum IneligibilityReason: Equatable {
            case noNotes
            case balanceTooLow
        }

        public enum VoteSubmissionStep: Equatable {
            case preparingProof     // syncVoteTree + generateVanWitness + buildVoteCommitment + signCastVote + submitVoteCommitment
            case confirming         // awaitCommitmentTreeGrowth
            case sendingShares      // buildSharePayloads + delegateShares

            public var label: String {
                switch self {
                case .preparingProof: return "Building vote proof..."
                case .confirming: return "Waiting for confirmation..."
                case .sendingShares: return "Sending to vote servers..."
                }
            }

            public var stepNumber: Int {
                switch self {
                case .preparingProof: return 1
                case .confirming: return 2
                case .sendingShares: return 3
                }
            }

            public static let totalSteps = 3
        }

        public var screenStack: [Screen] = [.loading]
        public var votingRound: VotingRound
        public var votes: [UInt32: VoteChoice] = [:]
        public var votingWeight: UInt64
        public var isKeystoneUser: Bool
        public var roundId: String
        public var activeSession: VotingSession?

        /// All rounds fetched from the server, sorted by snapshot height and numbered.
        public var allRounds: [RoundListItem] = []
        /// Computed: rounds that are active or tallying (newest first).
        public var activeRounds: [RoundListItem] {
            allRounds.filter { $0.session.status == .active || $0.session.status == .tallying }.reversed()
        }

        /// Computed: rounds that are finalized (newest first).
        public var completedRounds: [RoundListItem] {
            allRounds.filter { $0.session.status == .finalized }.reversed()
        }

        /// Resolved service config from CDN or local override.
        public var serviceConfig: VotingServiceConfig?

        /// Tally results for finalized rounds (proposalId → TallyResult).
        public var tallyResults: [UInt32: TallyResult] = [:]
        public var isLoadingTallyResults: Bool = false

        /// Reason the user can't participate (set when navigating to .ineligible).
        public var ineligibilityReason: IneligibilityReason?

        /// Wallet sync progress info for the walletSyncing screen.
        public var walletScannedHeight: UInt64 = 0

        /// Per-proposal share confirmation tracking (proposalId → confirmed count 0-4).
        public var shareConfirmations: [UInt32: Int] = [:]
        public var isPollingShareConfirmations: Bool = false

        /// Cached wallet notes from the snapshot query, used by delegation proof.
        public var walletNotes: [NoteInfo] = []

        /// Number of note bundles (groups of up to 4 notes). Set by setupBundles.
        public var bundleCount: UInt32 = 0

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

        /// Which bundle the Keystone signing loop is currently processing (0-based).
        public var currentKeystoneBundleIndex: UInt32 = 0

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
        /// Current step in the vote submission pipeline.
        public var voteSubmissionStep: VoteSubmissionStep?
        /// Error from the last vote submission attempt.
        public var voteSubmissionError: String?
        /// Which bundle is currently being voted (0-based), nil when not submitting.
        public var currentVoteBundleIndex: UInt32?
        /// Which proposal is currently being submitted, nil when idle.
        public var submittingProposalId: UInt32?

        /// Label for the current vote submission step, with bundle progress when applicable.
        public var voteSubmissionStepLabel: String? {
            guard let step = voteSubmissionStep else { return nil }
            if bundleCount > 1, let idx = currentVoteBundleIndex {
                let bundleLabel = "(\(idx + 1)/\(bundleCount))"
                switch step {
                case .preparingProof: return "Building vote proof \(bundleLabel)..."
                case .confirming: return "Waiting for confirmation \(bundleLabel)..."
                case .sendingShares: return "Sending to vote servers \(bundleLabel)..."
                }
            }
            return step.label
        }

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

        /// Whether the user can confirm a vote: delegation proof must be complete,
        /// bundle count must be known (restored from DB on resume), and no other
        /// vote can be in-flight (each vote needs the previous VAN committed).
        public var canConfirmVote: Bool {
            isDelegationReady && bundleCount > 0 && !isSubmittingVote
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
            votingRound: VotingRound = VotingRound(
                id: "", title: "", description: "", snapshotHeight: 0,
                snapshotDate: .now, votingStart: .now, votingEnd: .now, proposals: []
            ),
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
    let cancelStatusPollingId = UUID()
    let cancelSharePollingId = UUID()
    let cancelPipelineId = UUID()
    let cancelNewRoundPollingId = UUID()

    public enum Action: Equatable {
        // Navigation
        case dismissFlow
        case goBack
        case backToRoundsList

        // Rounds list
        case allRoundsLoaded([VotingSession])
        case roundTapped(String)
        case startNewRoundPolling

        // Initialization (DB, wallet notes, hotkey)
        case initialize
        case serviceConfigLoaded(VotingServiceConfig)
        case activeSessionLoaded(VotingSession)
        case noActiveRound
        case votingWeightLoaded(UInt64, [NoteInfo])
        case initializeFailed(String)
        case walletNotSynced(scannedHeight: UInt64, snapshotHeight: UInt64)
        case walletSyncProgressUpdated(UInt64)
        case hotkeyLoaded(String)
        case startActiveRoundPipeline

        // DB state stream (single source of truth)
        case votingDbStateChanged(VotingDbState)

        // Witness verification
        case verifyWitnesses
        case witnessPreparationStarted
        case rerunWitnessVerification
        case witnessVerificationCompleted([State.NoteWitnessResult], [WitnessData], State.WitnessTiming, UInt32)
        case witnessVerificationFailed(String)

        // Round resume check (skip delegation screen if already authorized)
        case roundResumeChecked(alreadyAuthorized: Bool)
        case bundleCountRestored(UInt32)

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
        case keystoneBundleAdvance
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
        case voteSubmissionBundleStarted(UInt32)
        case voteSubmissionStepUpdated(State.VoteSubmissionStep)
        case advanceAfterVote
        case backToList
        case nextProposalDetail
        case previousProposalDetail

        // Round status polling
        case startRoundStatusPolling
        case roundStatusUpdated(roundId: Data, SessionStatus)

        // Tally results
        case fetchTallyResults
        case tallyResultsLoaded([UInt32: TallyResult])

        // Share confirmation polling
        case startShareConfirmationPolling(UInt32)
        case shareConfirmationsUpdated(UInt32, Int)

        // Complete
        case doneTapped
    }

    public init() {}

    public var body: some Reducer<State, Action> {
        Reduce { state, action in
            switch action {
            // MARK: - Navigation

            case .dismissFlow:
                return .merge(
                    .cancel(id: cancelStateStreamId),
                    .cancel(id: cancelStatusPollingId),
                    .cancel(id: cancelSharePollingId),
                    .cancel(id: cancelPipelineId),
                    .cancel(id: cancelNewRoundPollingId)
                )

            case .goBack:
                if state.screenStack.count > 1 {
                    state.screenStack.removeLast()
                }
                return .none

            case .backToRoundsList:
                // Cancel per-round effects and re-fetch rounds (auto-navigates via allRoundsLoaded)
                state.screenStack = [.loading]
                // Reset per-round state
                state.activeSession = nil
                state.votes = [:]
                state.votingWeight = 0
                state.walletNotes = []
                state.noteWitnessResults = []
                state.cachedWitnesses = []
                state.witnessTiming = nil
                state.witnessStatus = .notStarted
                state.delegationProofStatus = .notStarted
                state.hotkeyAddress = nil
                state.pendingVote = nil
                state.isSubmittingVote = false
                state.submittingProposalId = nil
                state.voteSubmissionStep = nil
                state.voteSubmissionError = nil
                state.currentVoteBundleIndex = nil
                state.tallyResults = [:]
                state.isLoadingTallyResults = false
                state.ineligibilityReason = nil
                // Refresh the rounds list
                return .merge(
                    .cancel(id: cancelStateStreamId),
                    .cancel(id: cancelStatusPollingId),
                    .cancel(id: cancelSharePollingId),
                    .cancel(id: cancelPipelineId),
                    .cancel(id: cancelNewRoundPollingId),
                    .run { [votingAPI] send in
                        let allRounds = try await votingAPI.fetchAllRounds()
                        await send(.allRoundsLoaded(allRounds))
                    } catch: { error, _ in
                        print("[Voting] Failed to refresh rounds list: \(error)")
                    }
                )

            // MARK: - Rounds List

            case .allRoundsLoaded(let sessions):
                // Sort by created_at_height ascending for reliable creation order
                let sorted = sessions.sorted { $0.createdAtHeight < $1.createdAtHeight }
                state.allRounds = sorted.enumerated().map { index, session in
                    State.RoundListItem(roundNumber: index + 1, session: session)
                }

                // Auto-navigate: active round takes priority, else latest completed.
                // Skip if the user is already viewing the same round (guards against
                // onAppear re-firing .initialize and restarting the pipeline mid-vote).
                if let activeItem = state.activeRounds.first {
                    guard activeItem.id != state.activeSession?.voteRoundId.hexString else {
                        return .none
                    }
                    return .send(.roundTapped(activeItem.id))
                } else if let completedItem = state.completedRounds.first {
                    guard completedItem.id != state.activeSession?.voteRoundId.hexString else {
                        return .none
                    }
                    return .send(.roundTapped(completedItem.id))
                } else {
                    // No rounds — show empty state
                    state.screenStack = [.noRounds]
                    return .none
                }

            case .roundTapped(let roundId):
                guard let item = state.allRounds.first(where: { $0.id == roundId }) else { return .none }
                let session = item.session
                state.activeSession = session
                state.roundId = session.voteRoundId.hexString
                state.votingRound = sessionBackedRound(from: session, title: item.title, fallback: state.votingRound)
                reconcileProposalState(&state)

                switch session.status {
                case .active:
                    // Go straight to proposal list — the witness/proof pipeline
                    // runs in the background once voting weight is loaded.
                    state.screenStack = [.proposalList]
                    return .merge(
                        .cancel(id: cancelNewRoundPollingId),
                        .send(.startRoundStatusPolling),
                        // Defer pipeline start so SwiftUI renders the navigation
                        // transition before the reducer processes the pipeline action.
                        .run { send in await send(.startActiveRoundPipeline) }
                    )
                case .tallying:
                    state.screenStack = [.tallying]
                    return .send(.startRoundStatusPolling)
                case .finalized:
                    state.screenStack = [.results]
                    return .merge(
                        .send(.fetchTallyResults),
                        .send(.startNewRoundPolling)
                    )
                case .unspecified:
                    return .none
                }

            // MARK: - Initialization

            case .initialize:
                // Guard against onAppear re-firing while already initialized
                guard state.currentScreen == .loading else { return .none }
                return .run { [votingAPI] send in
                    // 1. Fetch service config (local override → CDN → deployed dev server fallback)
                    let config = try await votingAPI.fetchServiceConfig()
                    await send(.serviceConfigLoaded(config))
                } catch: { error, send in
                    print("[Voting] Service config fetch failed: \(error)")
                    await send(.serviceConfigLoaded(.fallback))
                }

            case .serviceConfigLoaded(let config):
                state.serviceConfig = config
                return .run { [votingAPI, votingCrypto] send in
                    // 2. Configure API client URLs
                    await votingAPI.configureURLs(config)

                    // 3. Open voting database
                    let dbPath = FileManager.default
                        .urls(for: .documentDirectory, in: .userDomainMask)[0]
                        .appendingPathComponent("voting.sqlite3").path
                    try await votingCrypto.openDatabase(dbPath)

                    // 4. Fetch all rounds and populate the list
                    let allRounds = try await votingAPI.fetchAllRounds()
                    print("[Voting] Fetched \(allRounds.count) rounds:")
                    for r in allRounds {
                        print("[Voting]   round=\(r.voteRoundId.hexString.prefix(16))... status=\(r.status) snapshot=\(r.snapshotHeight)")
                    }

                    await send(.allRoundsLoaded(allRounds))
                } catch: { error, send in
                    print("[Voting] Initialization failed: \(error)")
                    await send(.initializeFailed(error.localizedDescription))
                }

            case .startActiveRoundPipeline:
                guard let session = state.activeSession, session.status == .active else { return .none }
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                let snapshotHeight = session.snapshotHeight
                let roundId = session.voteRoundId.hexString
                return .run { [votingCrypto, mnemonic, walletStorage, sdkSynchronizer] send in
                    // Check wallet sync progress before querying notes
                    let walletScannedHeight = UInt64(sdkSynchronizer.latestState().latestBlockHeight)
                    if walletScannedHeight < snapshotHeight {
                        print("[Voting] Wallet scanned to \(walletScannedHeight), snapshot at \(snapshotHeight) — not synced yet")
                        await send(.walletNotSynced(scannedHeight: walletScannedHeight, snapshotHeight: snapshotHeight))
                        return
                    }

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
                    print("[Voting] Active round pipeline failed: \(error)")
                    await send(.initializeFailed(error.localizedDescription))
                }
                .cancellable(id: cancelPipelineId, cancelInFlight: true)

            case .activeSessionLoaded(let session):
                state.activeSession = session
                state.roundId = session.voteRoundId.hexString
                state.votingRound = sessionBackedRound(from: session, title: state.votingRound.title, fallback: state.votingRound)
                reconcileProposalState(&state)
                print("[Voting] activeSessionLoaded: status=\(session.status) round=\(session.voteRoundId.hexString.prefix(16))... proposals=\(session.proposals.count)")
                return .none

            case .noActiveRound:
                state.activeSession = nil
                state.screenStack = [.noRounds]
                return .none

            case .votingWeightLoaded(let weight, let notes):
                state.walletNotes = notes
                if notes.isEmpty {
                    state.votingWeight = 0
                    state.ineligibilityReason = .noNotes
                    state.screenStack = [.ineligible]
                    return .none
                }
                // Use smart bundling to determine eligible weight (excluding dust bundles)
                let bundleResult = notes.smartBundles()
                let eligibleWeight = bundleResult.eligibleWeight
                state.votingWeight = eligibleWeight
                if bundleResult.droppedCount > 0 {
                    print("[Voting] Smart bundling: dropped \(bundleResult.droppedCount) notes in sub-threshold bundles (eligible: \(eligibleWeight) of \(weight) total)")
                }
                if eligibleWeight < 12_500_000 {
                    state.ineligibilityReason = .balanceTooLow
                    state.screenStack = [.ineligible]
                    return .none
                }
                // Show proposals immediately while witnesses load in the background.
                // This avoids a 10–20s blank spinner waiting for the tree state fetch.
                // Don't set delegationProofStatus here — verifyWitnesses will set it
                // only for fresh rounds, avoiding a brief flash for cached rounds.
                state.screenStack = [.proposalList]
                return .merge(
                    .publisher {
                        votingCrypto.stateStream()
                            .receive(on: DispatchQueue.main)
                            .map(Action.votingDbStateChanged)
                    }
                    .cancellable(id: cancelStateStreamId, cancelInFlight: true),
                    .send(.verifyWitnesses)
                )

            case .initializeFailed(let error):
                print("[Voting] Initialization error: \(error)")
                state.screenStack = [.error(error)]
                return .none

            case .walletNotSynced(let scannedHeight, let snapshotHeight):
                state.walletScannedHeight = scannedHeight
                state.screenStack = [.walletSyncing]
                // Poll sync progress and auto-retry the pipeline once caught up
                return .run { [sdkSynchronizer] send in
                    while !Task.isCancelled {
                        try await Task.sleep(for: .seconds(2))
                        let height = UInt64(sdkSynchronizer.latestState().latestBlockHeight)
                        await send(.walletSyncProgressUpdated(height))
                        if height >= snapshotHeight {
                            await send(.startActiveRoundPipeline)
                            return
                        }
                    }
                } catch: { _, _ in }
                .cancellable(id: cancelPipelineId, cancelInFlight: true)

            case .walletSyncProgressUpdated(let height):
                state.walletScannedHeight = height
                return .none

            case .hotkeyLoaded(let address):
                state.hotkeyAddress = address
                return .none

            // MARK: - Round Status Polling

            case .startRoundStatusPolling:
                guard let session = state.activeSession else { return .none }
                let roundIdHex = session.voteRoundId.hexString
                return .run { [votingAPI] send in
                    while !Task.isCancelled {
                        try await Task.sleep(for: .seconds(5))
                        let updated = try await votingAPI.fetchRoundById(roundIdHex)
                        await send(.roundStatusUpdated(roundId: updated.voteRoundId, updated.status))
                    }
                } catch: { error, _ in
                    print("[Voting] Status polling error: \(error)")
                }
                .cancellable(id: cancelStatusPollingId, cancelInFlight: true)

            case .roundStatusUpdated(let polledRoundId, let newStatus):
                guard let session = state.activeSession else { return .none }

                // Guard against stale poll responses from a previously viewed
                // round arriving after the user navigated to a different round.
                // TCA effect cancellation is cooperative, so a queued action
                // from the old poll can slip through.
                guard polledRoundId == session.voteRoundId else {
                    print("[Voting] roundStatusUpdated: ignoring stale poll for \(polledRoundId.hexString.prefix(16))..., active round is \(session.voteRoundId.hexString.prefix(16))...")
                    return .none
                }

                // Only react to actual transitions
                print("[Voting] roundStatusUpdated: old=\(session.status) new=\(newStatus)")
                guard newStatus != session.status else { return .none }

                // Update session status
                let updatedSession = VotingSession(
                    voteRoundId: session.voteRoundId,
                    snapshotHeight: session.snapshotHeight,
                    snapshotBlockhash: session.snapshotBlockhash,
                    proposalsHash: session.proposalsHash,
                    voteEndTime: session.voteEndTime,
                    eaPK: session.eaPK,
                    vkZkp1: session.vkZkp1,
                    vkZkp2: session.vkZkp2,
                    vkZkp3: session.vkZkp3,
                    ncRoot: session.ncRoot,
                    nullifierIMTRoot: session.nullifierIMTRoot,
                    creator: session.creator,
                    description: session.description,
                    proposals: session.proposals,
                    status: newStatus,
                    createdAtHeight: session.createdAtHeight,
                    title: session.title
                )
                state.activeSession = updatedSession

                // Also update the corresponding entry in allRounds so the list stays consistent
                if let idx = state.allRounds.firstIndex(where: { $0.session.voteRoundId == session.voteRoundId }) {
                    state.allRounds[idx] = State.RoundListItem(
                        roundNumber: state.allRounds[idx].roundNumber,
                        session: updatedSession
                    )
                }

                switch newStatus {
                case .tallying:
                    state.screenStack = [.tallying]
                    return .none
                case .finalized:
                    state.screenStack = [.results]
                    return .merge(
                        .cancel(id: cancelStatusPollingId),
                        .send(.fetchTallyResults),
                        .send(.startNewRoundPolling)
                    )
                default:
                    return .none
                }

            // MARK: - New Round Polling (after finalization)

            case .startNewRoundPolling:
                return .run { [votingAPI] send in
                    while !Task.isCancelled {
                        try await Task.sleep(for: .seconds(30))
                        let allRounds = try await votingAPI.fetchAllRounds()
                        let hasActive = allRounds.contains { $0.status == .active || $0.status == .tallying }
                        if hasActive {
                            await send(.allRoundsLoaded(allRounds))
                        }
                    }
                } catch: { _, _ in }
                .cancellable(id: cancelNewRoundPollingId, cancelInFlight: true)

            // MARK: - Tally Results

            case .fetchTallyResults:
                guard let session = state.activeSession else { return .none }
                state.isLoadingTallyResults = true
                let roundIdHex = session.voteRoundId.hexString
                return .run { [votingAPI] send in
                    let results = try await votingAPI.fetchTallyResults(roundIdHex)
                    await send(.tallyResultsLoaded(results))
                } catch: { error, send in
                    print("[Voting] Failed to fetch tally results: \(error)")
                    await send(.tallyResultsLoaded([:]))
                }

            case .tallyResultsLoaded(let results):
                state.tallyResults = results
                state.isLoadingTallyResults = false
                return .none

            // MARK: - Share Confirmation Polling

            case .startShareConfirmationPolling(let proposalId):
                state.isPollingShareConfirmations = true
                guard let session = state.activeSession else { return .none }
                let roundIdHex = session.voteRoundId.hexString
                return .run { [votingAPI] send in
                    while !Task.isCancelled {
                        try await Task.sleep(for: .seconds(5))
                        // Check tally endpoint for reveal-share counts
                        let tally = try await votingAPI.fetchProposalTally(
                            dataFromHex(roundIdHex), proposalId
                        )
                        let totalShares = tally.entries.reduce(0) { $0 + Int($1.amount) }
                        await send(.shareConfirmationsUpdated(proposalId, min(totalShares, 4)))
                    }
                } catch: { error, _ in
                    print("[Voting] Share confirmation polling error: \(error)")
                }
                .cancellable(id: cancelSharePollingId, cancelInFlight: true)

            case .shareConfirmationsUpdated(let proposalId, let count):
                state.shareConfirmations[proposalId] = count
                if count >= 4 {
                    state.isPollingShareConfirmations = false
                    return .cancel(id: cancelSharePollingId)
                }
                return .none

            // MARK: - Witness Verification

            case .verifyWitnesses:
                guard let activeSession = state.activeSession else {
                    state.witnessStatus = .failed("missing active session")
                    return .none
                }
                state.witnessTiming = nil
                let roundId = activeSession.voteRoundId.hexString
                let snapshotHeight = activeSession.snapshotHeight
                let notes = state.walletNotes
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                return .run { [sdkSynchronizer, votingCrypto] send in
                    // Check if this round already exists and ALL bundles have proofs
                    let existingState = try? await votingCrypto.getRoundState(roundId)
                    let alreadyAuthorized = existingState?.proofGenerated ?? false

                    if alreadyAuthorized {
                        await send(.roundResumeChecked(alreadyAuthorized: true))
                        return
                    }

                    // Fresh round — show witness preparation status
                    await send(.witnessPreparationStarted)

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
                        ), 0))
                        return
                    }

                    // Setup bundles (value-aware split into groups of up to 4)
                    let setupResult = try await votingCrypto.setupBundles(roundId, notes)
                    let bundleCount = setupResult.bundleCount
                    print("[Voting] Setup \(bundleCount) bundle(s) for \(notes.count) notes (eligible weight: \(setupResult.eligibleWeight))")

                    // Phase 1: Fetch tree state from lightwalletd
                    let t0 = ContinuousClock.now
                    let treeStateBytes = try await sdkSynchronizer.getTreeState(snapshotHeight)
                    try await votingCrypto.storeTreeState(roundId, treeStateBytes)
                    let t1 = ContinuousClock.now
                    let fetchMs = UInt64(t0.duration(to: t1).components.seconds * 1000)
                        + UInt64(t0.duration(to: t1).components.attoseconds / 1_000_000_000_000_000)
                    print("[Voting] Tree state fetch: \(fetchMs)ms")

                    // Phase 2: Generate witnesses per-bundle (includes Rust-side verification)
                    let noteChunks = notes.smartBundles().bundles
                    var allWitnesses: [WitnessData] = []
                    for bundleIndex in 0..<bundleCount {
                        let chunkNotes = noteChunks[Int(bundleIndex)]
                        let witnesses = try await votingCrypto.generateNoteWitnesses(
                            roundId, bundleIndex, walletDbPath, chunkNotes
                        )
                        allWitnesses.append(contentsOf: witnesses)
                    }
                    let t2 = ContinuousClock.now
                    let genMs = UInt64(t1.duration(to: t2).components.seconds * 1000)
                        + UInt64(t1.duration(to: t2).components.attoseconds / 1_000_000_000_000_000)
                    print("[Voting] Witness generation: \(genMs)ms (\(allWitnesses.count) notes)")

                    // Phase 3: Verify each witness on Swift side for UI display
                    let sortedNotes = noteChunks.flatMap { $0 }
                    var results: [Voting.State.NoteWitnessResult] = []
                    for (i, witness) in allWitnesses.enumerated() {
                        let verified = (try? await votingCrypto.verifyWitness(witness)) ?? false
                        let note = sortedNotes[i]
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
                    await send(.witnessVerificationCompleted(results, allWitnesses, timing, bundleCount))
                } catch: { error, send in
                    print("[Voting] Witness verification failed: \(error)")
                    await send(.witnessVerificationFailed(error.localizedDescription))
                }

            case .witnessPreparationStarted:
                // Only shown for fresh rounds (not cached). This avoids a brief
                // flash of "Preparing note witnesses..." when resuming a round.
                state.witnessStatus = .inProgress
                state.delegationProofStatus = .generating(progress: 0)
                return .none

            case .rerunWitnessVerification:
                // Invalidate cached witnesses and re-run from scratch
                state.noteWitnessResults = []
                state.cachedWitnesses = []
                state.witnessTiming = nil
                return .send(.verifyWitnesses)

            case .witnessVerificationCompleted(let results, let witnesses, let timing, let bundleCount):
                state.noteWitnessResults = results
                state.cachedWitnesses = witnesses
                state.witnessTiming = timing
                state.witnessStatus = .completed
                state.bundleCount = bundleCount
                // Non-Keystone users skip the delegation signing screen entirely.
                // Screen is already on .proposalList (set early in .votingWeightLoaded).
                if !state.isKeystoneUser {
                    return .send(.startDelegationProof)
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
                    // Restore bundleCount from the DB so vote casting knows how many bundles to iterate.
                    // Start state stream to sync votes and hotkey from the existing round,
                    // then trigger a refresh so the current DB state is published
                    // (stateStream uses dropFirst, so without this the existing value is lost).
                    let roundId = state.roundId
                    return .merge(
                        .run { [votingCrypto] send in
                            let count = try await votingCrypto.getBundleCount(roundId)
                            await send(.bundleCountRestored(count))
                        } catch: { error, send in
                            print("[Voting] Failed to restore bundle count: \(error)")
                            await send(.witnessVerificationFailed("Failed to restore voting state: \(error.localizedDescription)"))
                        },
                        .publisher {
                            votingCrypto.stateStream()
                                .receive(on: DispatchQueue.main)
                                .map(Action.votingDbStateChanged)
                        }
                        .cancellable(id: cancelStateStreamId, cancelInFlight: true),
                        .run { _ in
                            await votingCrypto.refreshState(roundId)
                        }
                    )
                }
                return .none

            case .bundleCountRestored(let count):
                state.bundleCount = count
                return .none

            // MARK: - DB State Stream

            case .votingDbStateChanged(let dbState):
                // Votes: DB is source of truth, but preserve optimistic vote during submission
                var mergedVotes = dbState.votesByProposal
                if state.isSubmittingVote {
                    for (proposalId, choice) in state.votes where mergedVotes[proposalId] == nil {
                        mergedVotes[proposalId] = choice
                    }
                }
                state.votes = mergedVotes
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
                state.currentKeystoneBundleIndex = 0
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
                // IMT server URL from resolved service config
                let imtServerUrl = state.serviceConfig?.nullifierProviders.first?.url ?? "https://46-101-255-48.sslip.io/nullifier"
                let keystoneBundleIndex = state.currentKeystoneBundleIndex
                let bundleCount = state.bundleCount
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
                            guard bundleCount > 0 else {
                                await send(.delegationProofCompleted)
                                return
                            }
                            // Build governance PCZT for the current bundle — its single Orchard
                            // action IS the governance dummy action, so Keystone's SpendAuth
                            // signature will verify against the PCZT's ZIP-244 sighash.
                            let noteChunks = cachedNotes.smartBundles().bundles
                            let bundleNotes = noteChunks[Int(keystoneBundleIndex)]
                            print("[Voting] Keystone: preparing PCZT for bundle \(keystoneBundleIndex + 1)/\(bundleCount)")
                            let govPczt = try await votingCrypto.buildGovernancePczt(
                                roundId,
                                keystoneBundleIndex,
                                bundleNotes,
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

                        // Non-Keystone path: iterate bundles, build and prove delegation for each
                        let noteChunks = cachedNotes.smartBundles().bundles
                        let bundleCount = UInt32(noteChunks.count)

                        for bundleIndex: UInt32 in 0..<bundleCount {
                            let bundleNotes = noteChunks[Int(bundleIndex)]
                            print("[Voting] Delegation bundle \(bundleIndex + 1)/\(bundleCount) (\(bundleNotes.count) notes)")

                            for try await event in votingCrypto.buildAndProveDelegation(
                                roundId, bundleIndex, bundleNotes, walletDbPath, senderSeed, hotkeySeed,
                                networkId, accountIndex, imtServerUrl
                            ) {
                                switch event {
                                case .progress(let p):
                                    // Scale progress: each bundle contributes 1/bundleCount of total
                                    let overallProgress = (Double(bundleIndex) + p) / Double(bundleCount)
                                    print("[Voting] ZKP #1 bundle \(bundleIndex) progress: \(Int(p * 100))%")
                                    await send(.delegationProofProgress(overallProgress))
                                case .completed(let proof):
                                    print("[Voting] ZKP #1 bundle \(bundleIndex) COMPLETE — proof size: \(proof.count) bytes")
                                }
                            }

                            // Submit delegation TX for this bundle
                            let registration = try await votingCrypto.getDelegationSubmission(
                                roundId, bundleIndex, senderSeed, networkId, accountIndex
                            )
                            let preTree = try await votingAPI.fetchLatestCommitmentTree()
                            let delegTxResult = try await votingAPI.submitDelegation(registration)
                            print("[Voting] Delegation TX \(bundleIndex) submitted: \(delegTxResult.txHash)")

                            // Poll until the delegation TX lands and the tree grows
                            let postTree = try await votingAPI.awaitCommitmentTreeGrowth(preTree.nextIndex, 30)
                            let vanPosition = UInt32(postTree.nextIndex) - 1
                            try await votingCrypto.storeVanPosition(roundId, bundleIndex, vanPosition)
                            print("[Voting] VAN position stored for bundle \(bundleIndex): \(vanPosition)")
                        }

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
                let cachedNotes = state.walletNotes
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                let accountIndex: UInt32 = 0
                // IMT server URL from resolved service config
                let imtServerUrl = state.serviceConfig?.nullifierProviders.first?.url ?? "https://46-101-255-48.sslip.io/nullifier"
                let keystoneBundleIndex = state.currentKeystoneBundleIndex
                let bundleCount = state.bundleCount
                return .run { [votingCrypto, votingAPI, mnemonic, walletStorage] send in
                    let senderPhrase = try walletStorage.exportWallet().seedPhrase.value()
                    let senderSeed = try mnemonic.toSeed(senderPhrase)
                    let hotkeyPhrase = try walletStorage.exportVotingHotkey().seedPhrase.value()
                    let hotkeySeed = try mnemonic.toSeed(hotkeyPhrase)

                    // Use cached wallet notes for the current bundle
                    let noteChunks = cachedNotes.smartBundles().bundles
                    let bundleNotes = noteChunks[Int(keystoneBundleIndex)]

                    print("[Voting] Keystone: proving bundle \(keystoneBundleIndex + 1)/\(bundleCount)")
                    for try await event in votingCrypto.buildAndProveDelegation(
                        roundId, keystoneBundleIndex, bundleNotes, walletDbPath, senderSeed, hotkeySeed,
                        networkId, accountIndex, imtServerUrl
                    ) {
                        switch event {
                        case .progress(let p):
                            // Scale progress: each bundle contributes 1/bundleCount of total
                            let overallProgress = (Double(keystoneBundleIndex) + p) / Double(bundleCount)
                            print("[Voting] ZKP #1 bundle \(keystoneBundleIndex) progress: \(Int(p * 100))%")
                            await send(.delegationProofProgress(overallProgress))
                        case .completed(let proof):
                            print("[Voting] ZKP #1 bundle \(keystoneBundleIndex) COMPLETE — proof size: \(proof.count) bytes")
                        }
                    }

                    // Submit delegation TX for this bundle
                    let registration = try await votingCrypto.getDelegationSubmission(
                        roundId, keystoneBundleIndex, senderSeed, networkId, accountIndex
                    )
                    let preTree = try await votingAPI.fetchLatestCommitmentTree()
                    let delegTxResult = try await votingAPI.submitDelegation(registration)
                    print("[Voting] Delegation TX \(keystoneBundleIndex) submitted: \(delegTxResult.txHash)")

                    // Poll until the delegation TX lands and the tree grows
                    let postTree = try await votingAPI.awaitCommitmentTreeGrowth(preTree.nextIndex, 30)
                    let vanPosition = UInt32(postTree.nextIndex) - 1
                    try await votingCrypto.storeVanPosition(roundId, keystoneBundleIndex, vanPosition)
                    print("[Voting] VAN position stored for bundle \(keystoneBundleIndex): \(vanPosition)")

                    // If more bundles remain, advance to next; otherwise complete
                    if keystoneBundleIndex + 1 < bundleCount {
                        await send(.keystoneBundleAdvance)
                    } else {
                        await send(.delegationProofCompleted)
                    }
                } catch: { error, send in
                    await send(.delegationProofFailed(error.localizedDescription))
                }

            case .keystoneBundleAdvance:
                // Move to the next bundle and loop back into the Keystone signing flow
                state.currentKeystoneBundleIndex += 1
                return .send(.startDelegationProof)

            case .spendAuthSignatureExtractionFailed(let error):
                state.keystoneSigningStatus = .failed(error)
                return .none

            case .delegationProofProgress(let progress):
                state.delegationProofStatus = .generating(progress: progress)
                return .none

            case .delegationProofCompleted:
                state.delegationProofStatus = .complete
                state.currentKeystoneBundleIndex = 0
                return .none

            case .delegationProofFailed(let error):
                state.currentKeystoneBundleIndex = 0
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
                // If already confirmed or a submission is in-flight, ignore
                guard state.votes[proposalId] == nil, !state.isSubmittingVote else { return .none }
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
                state.submittingProposalId = pending.proposalId
                state.voteSubmissionError = nil
                state.lastVoteCommitmentTxHash = nil

                let proposalId = pending.proposalId
                let choice = pending.choice
                let numOptions = UInt32(state.votingRound.proposals.first { $0.id == proposalId }?.options.count ?? 3)
                let roundId = state.roundId
                let network = zcashSDKEnvironment.network
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                let chainNodeUrl = state.serviceConfig?.voteServers.first?.url ?? "https://46-101-255-48.sslip.io"

                let bundleCount = state.bundleCount
                return .run { [votingAPI, votingCrypto, mnemonic, walletStorage] send in
                    let hotkeyPhrase = try walletStorage.exportVotingHotkey().seedPhrase.value()
                    let hotkeySeed = try mnemonic.toSeed(hotkeyPhrase)

                    // Iterate bundles: each bundle has its own VAN and casts its own vote.
                    // Skip already-submitted bundles (enables retry after partial failure).
                    let existingVotes = try await votingCrypto.getVotes(roundId)
                    let submittedBundles = Set(
                        existingVotes
                            .filter { $0.proposalId == proposalId && $0.submitted }
                            .map(\.bundleIndex)
                    )
                    for bundleIndex: UInt32 in 0..<bundleCount {
                        if submittedBundles.contains(bundleIndex) {
                            print("[Voting] Vote bundle \(bundleIndex + 1)/\(bundleCount) already submitted, skipping")
                            continue
                        }
                        print("[Voting] Vote bundle \(bundleIndex + 1)/\(bundleCount) for proposal \(proposalId)")

                        // Update progress per-bundle so the UI shows which bundle is being processed
                        await send(.voteSubmissionBundleStarted(bundleIndex))

                        // Sync vote commitment tree from chain and generate VAN witness.
                        // Requires storeVanPosition to have been called after delegation TX.
                        let anchorHeight = try await votingCrypto.syncVoteTree(roundId, chainNodeUrl)
                        let vanWitness = try await votingCrypto.generateVanWitness(roundId, bundleIndex, anchorHeight)
                        print("[Voting] VAN witness: position=\(vanWitness.position), anchor=\(vanWitness.anchorHeight)")

                        // Build vote commitment + ZKP #2 (stored in DB).
                        // The builder internally decomposes weight, encrypts shares under EA pk,
                        // and returns encrypted shares in the bundle.
                        var builtBundle: VoteCommitmentBundle?
                        for try await event in votingCrypto.buildVoteCommitment(
                            roundId, bundleIndex, hotkeySeed, networkId, proposalId, choice,
                            numOptions, vanWitness.authPath, vanWitness.position, vanWitness.anchorHeight
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
                        await send(.voteSubmissionStepUpdated(.confirming))
                        let preVCTree = try await votingAPI.fetchLatestCommitmentTree()
                        let txResult = try await votingAPI.submitVoteCommitment(builtBundle, castVoteSig)
                        await send(.voteCommitmentSubmitted(txResult.txHash))

                        // Wait for the cast-vote TX to land and read the new tree position.
                        // The chain appends vote_authority_note_new first, then vote_commitment,
                        // so the new VAN is at nextIndex-2 and the VC is at nextIndex-1.
                        let postVCTree = try await votingAPI.awaitCommitmentTreeGrowth(preVCTree.nextIndex, 30)
                        let newVanPosition = UInt32(postVCTree.nextIndex) - 2
                        let vcTreePosition = postVCTree.nextIndex - 1

                        // Update VAN position so the next vote on this bundle uses the new VAN leaf
                        try await votingCrypto.storeVanPosition(roundId, bundleIndex, newVanPosition)

                        await send(.voteSubmissionStepUpdated(.sendingShares))
                        let payloads = try await votingCrypto.buildSharePayloads(
                            builtBundle.encShares, builtBundle, choice, numOptions, vcTreePosition
                        )
                        // Retry share delegation up to 3 times — helper servers may return 503 transiently
                        var lastShareError: Error?
                        for attempt in 1...3 {
                            do {
                                try await votingAPI.delegateShares(payloads, roundId)
                                lastShareError = nil
                                break
                            } catch {
                                lastShareError = error
                                print("[Voting] delegateShares attempt \(attempt)/3 failed: \(error)")
                                if attempt < 3 {
                                    try await Task.sleep(for: .seconds(2))
                                }
                            }
                        }
                        if let lastShareError { throw lastShareError }

                        // Mark vote submitted in DB for this bundle
                        try await votingCrypto.markVoteSubmitted(roundId, bundleIndex, proposalId)
                    }

                    // All bundles voted — advance to proposal list
                    await send(.advanceAfterVote)
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
                state.submittingProposalId = nil
                state.voteSubmissionStep = nil
                state.voteSubmissionError = error
                state.currentVoteBundleIndex = nil
                // Remove the optimistic vote since it didn't land on chain
                state.votes.removeValue(forKey: proposalId)
                return .none

            case .voteSubmissionBundleStarted(let index):
                state.currentVoteBundleIndex = index
                state.voteSubmissionStep = .preparingProof
                return .none

            case .voteSubmissionStepUpdated(let step):
                state.voteSubmissionStep = step
                return .none

            case .advanceAfterVote:
                state.isSubmittingVote = false
                state.submittingProposalId = nil
                state.voteSubmissionStep = nil
                state.voteSubmissionError = nil
                state.currentVoteBundleIndex = nil
                // Return to proposal list so the user can pick their next vote freely.
                if case .proposalDetail = state.currentScreen {
                    state.screenStack.removeLast()
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
                state.screenStack = [.proposalList]
                return .none
            }
        }
        .ifLet(\.$keystoneScan, action: \.keystoneScan) {
            Scan()
        }
    }

    private func sessionBackedRound(from session: VotingSession, title: String, fallback: VotingRound) -> VotingRound {
        let proposals = session.proposals.isEmpty ? fallback.proposals : session.proposals
        // Prefer the on-chain title, then the caller-provided title, then the fallback
        let resolvedTitle = !session.title.isEmpty ? session.title : (!title.isEmpty ? title : fallback.title)
        return VotingRound(
            id: session.voteRoundId.hexString,
            title: resolvedTitle,
            description: session.description.isEmpty ? fallback.description : session.description,
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

// MARK: - Note Bundling

/// Result of value-aware note bundling on the Swift side.
private struct BundleResult {
    let bundles: [[NoteInfo]]
    let eligibleWeight: UInt64
    let droppedCount: Int
}

private extension Array where Element == NoteInfo {
    /// Ballot divisor — must match `librustvoting::governance::BALLOT_DIVISOR`.
    static var ballotDivisor: UInt64 { 12_500_000 }

    /// Value-aware bundling using greedy min-total assignment.
    ///
    /// Algorithm mirrors the Rust `chunk_notes` for client-side use:
    /// 1. Sort notes by value DESC, then position ASC as tiebreaker
    /// 2. Fill bundles sequentially to capacity (4 notes each)
    /// 3. Drop bundles with total < ballotDivisor
    /// 4. Re-sort notes within each surviving bundle by position
    /// 5. Sort surviving bundles by their minimum note position
    func smartBundles() -> BundleResult {
        guard !isEmpty else {
            return BundleResult(bundles: [], eligibleWeight: 0, droppedCount: 0)
        }

        // Step 1: Sort by value DESC, then position ASC
        let sorted = self.sorted { lhs, rhs in
            if lhs.value != rhs.value { return lhs.value > rhs.value }
            return lhs.position < rhs.position
        }

        // Step 2: Fill bundles sequentially to capacity (4 notes each)
        var bundleNotes: [[NoteInfo]] = []
        var bundleTotals: [UInt64] = []

        for note in sorted {
            if bundleNotes.isEmpty || bundleNotes.last!.count >= 4 {
                bundleNotes.append([])
                bundleTotals.append(0)
            }
            let last = bundleNotes.count - 1
            bundleTotals[last] += note.value
            bundleNotes[last].append(note)
        }

        // Step 3: Drop bundles with total < ballotDivisor
        let numBundles = bundleNotes.count
        var surviving: [[NoteInfo]] = []
        var eligibleWeight: UInt64 = 0
        var survivingNoteCount = 0

        for i in 0..<numBundles {
            if bundleTotals[i] >= Self.ballotDivisor {
                surviving.append(bundleNotes[i])
                // Quantize per bundle: VAN weight = floor(total / ballotDivisor) * ballotDivisor
                eligibleWeight += (bundleTotals[i] / Self.ballotDivisor) * Self.ballotDivisor
                survivingNoteCount += bundleNotes[i].count
            }
        }
        let droppedCount = count - survivingNoteCount

        // Step 5: Re-sort notes within each surviving bundle by position
        for i in 0..<surviving.count {
            surviving[i].sort { $0.position < $1.position }
        }

        // Step 6: Sort surviving bundles by their minimum note position
        surviving.sort { ($0.first?.position ?? .max) < ($1.first?.position ?? .max) }

        return BundleResult(bundles: surviving, eligibleWeight: eligibleWeight, droppedCount: droppedCount)
    }
}

/// Convert hex string to Data (used for share confirmation polling).
private func dataFromHex(_ hex: String) -> Data {
    var data = Data()
    var idx = hex.startIndex
    while idx < hex.endIndex {
        let next = hex.index(idx, offsetBy: 2, limitedBy: hex.endIndex) ?? hex.endIndex
        if let byte = UInt8(hex[idx..<next], radix: 16) {
            data.append(byte)
        }
        idx = next
    }
    return data
}
