// swiftlint:disable file_length
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
import os

private let logger = Logger(subsystem: "co.zodl.voting", category: "VotingStore")

private enum VotingFlowError: LocalizedError {
    case missingActiveSession
    case missingSigningAccount
    case missingHotkeyAddress
    case missingPendingUnsignedPczt
    case invalidDelegationSignature
    case missingVoteCommitmentBundle
    case voteCommitmentTxFailed(code: UInt32)

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
            return "Keystone delegation signature tuple (rk, sighash, sig) is inconsistent with the payload being submitted."
        case .missingVoteCommitmentBundle:
            return "vote commitment build completed without a commitment bundle"
        case .voteCommitmentTxFailed(code: let code):
            return "vote commitment TX failed on-chain (code \(code))"
        }
    }
}

private enum VotingErrorMapper {
    static func userFriendlyMessage(from rawError: String) -> String {
        if rawError.contains("nullifier already spent") {
            return "This wallet has already been registered for this voting round. "
                + "If you have this wallet on multiple devices, please use the device "
                + "where you originally started the voting process."
        }
        if rawError.contains("vote round is not active") {
            return "This voting round is no longer accepting votes. It may have ended or is not yet open."
        }
        if rawError.contains("vote round not found") {
            return "This voting round could not be found on the network."
        }
        if rawError.contains("PIR server connect failed") || rawError.contains("PIR parallel fetch failed") {
            return "Unable to reach the nullifier service. Please check your internet connection and try again."
        }
        if rawError.contains("Commitment tree did not grow") {
            return "Your transaction hasn't been confirmed yet. The network may be congested — please wait a moment and try again."
        }
        if rawError.contains("invalid commitment tree anchor height") {
            return "The voting state is out of sync. Please retry to use the latest data."
        }
        if rawError.contains("invalid zero-knowledge proof") {
            return "Your proof was rejected by the network. Please try again."
        }
        if rawError.contains("delegation bundle build failed") || rawError.contains("create_proof failed") {
            return "Proof generation failed. Please try again."
        }
        if rawError.contains("NoTreeState") || rawError.contains("no tree state") {
            return "The voting commitment tree is not available yet. The voting round may not have started or the chain has not processed any commitments."
        }
        if rawError.contains("HTTP 5") {
            return "The voting server is temporarily unavailable. Please try again shortly."
        }
        if rawError.contains("GRPCStatus") || rawError.contains("RPC timed out") || rawError.contains("Transport became inactive") {
            return "Unable to reach the Zcash lightwalletd server. Please check your internet connection or try switching to a different server in Settings."
        }
        return rawError
    }
}

@Reducer
public struct Voting { // swiftlint:disable:this type_body_length
    @Dependency(\.databaseFiles)
    var databaseFiles
    @Dependency(\.keystoneHandler)
    var keystoneHandler
    @Dependency(\.mnemonic)
    var mnemonic
    @Dependency(\.pasteboard)
    var pasteboard
    @Dependency(\.sdkSynchronizer)
    var sdkSynchronizer
    @Dependency(\.votingAPI)
    var votingAPI
    @Dependency(\.votingCrypto)
    var votingCrypto
    @Dependency(\.walletStorage)
    var walletStorage
    @Dependency(\.zcashSDKEnvironment)
    var zcashSDKEnvironment
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

        /// Per-proposal share confirmation tracking (proposalId → confirmed count 0-5).
        public var shareConfirmations: [UInt32: Int] = [:]
        public var isPollingShareConfirmations: Bool = false

        /// Cached wallet notes from the snapshot query, used by delegation proof.
        public var walletNotes: [NoteInfo] = []

        /// Number of note bundles (groups of up to 5 notes). Set by setupBundles.
        public var bundleCount: UInt32 = 0

        /// Hotkey address derived from keychain mnemonic, shown on delegation signing screen.
        public var hotkeyAddress: String?

        @Shared(.inMemory(.selectedWalletAccount))
        public var selectedWalletAccount: WalletAccount?
        @Shared(.inMemory(.toast))
        public var toast: Toast.Edge?

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
        /// True while the delegation proof `.run` effect is in-flight. Guards against
        /// re-entrant `.startDelegationProof` dispatches from round polling re-triggers.
        public var isDelegationProofInFlight: Bool = false
        public var keystoneSigningStatus: KeystoneSigningStatus = .idle

        /// Which bundle the Keystone signing loop is currently processing (0-based).
        public var currentKeystoneBundleIndex: UInt32 = 0

        /// Per-bundle Keystone signature data collected during the multi-bundle signing loop.
        public struct KeystoneBundleSignature: Equatable {
            public let sig: Data
            public let sighash: Data
            public let rk: Data // swiftlint:disable:this identifier_name
        }

        /// Collected Keystone signatures for each bundle, accumulated during the signing loop.
        public var keystoneBundleSignatures: [KeystoneBundleSignature] = []

        /// Governance PCZT result for Keystone signing flow (contains metadata + pczt_bytes).
        public var pendingGovernancePczt: GovernancePcztResult?
        /// Unsigned delegation PCZT request shown as QR and used for signature extraction.
        public var pendingUnsignedDelegationPczt: Pczt?
        @Presents public var keystoneScan: Scan.State?
        @Presents public var skipBundlesAlert: AlertState<Action>?

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

        /// ZEC value for the current Keystone bundle (or total if single-bundle / non-Keystone).
        public var currentBundleZECString: String? {
            guard isKeystoneUser, bundleCount > 1 else { return nil }
            let bundles = walletNotes.smartBundles().bundles
            let idx = Int(currentKeystoneBundleIndex)
            guard idx < bundles.count else { return nil }
            let weight = bundles[idx].reduce(UInt64(0)) { $0 + $1.value }
            return String(format: "%.2f", Double(weight) / 100_000_000.0)
        }

        /// Total ZEC weight already signed across collected Keystone bundle signatures.
        public var signedBundlesZECString: String {
            let bundles = walletNotes.smartBundles().bundles
            let signedWeight = keystoneBundleSignatures.indices.reduce(UInt64(0)) { total, i in
                guard i < bundles.count else { return total }
                return total + bundles[i].reduce(UInt64(0)) { $0 + $1.value }
            }
            return String(format: "%.2f", Double(signedWeight) / 100_000_000.0)
        }

        /// Total ZEC weight in unsigned bundles that would be given up by skipping.
        public var skippedBundlesZECString: String {
            let bundles = walletNotes.smartBundles().bundles
            let signedCount = keystoneBundleSignatures.count
            let skippedWeight = (signedCount..<bundles.count).reduce(UInt64(0)) { total, i in
                total + bundles[i].reduce(UInt64(0)) { $0 + $1.value }
            }
            return String(format: "%.2f", Double(skippedWeight) / 100_000_000.0)
        }

        /// Raw ZEC weight for the memo — per-bundle for Keystone multi-bundle, total otherwise.
        public var memoWeightZatoshi: UInt64 {
            if isKeystoneUser, bundleCount > 1 {
                let bundles = walletNotes.smartBundles().bundles
                let idx = Int(currentKeystoneBundleIndex)
                if idx < bundles.count {
                    return bundles[idx].reduce(UInt64(0)) { $0 + $1.value }
                }
            }
            return walletNotes.reduce(UInt64(0)) { $0 + $1.value }
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
                id: "",
                title: "",
                description: "",
                snapshotHeight: 0,
                snapshotDate: .now,
                votingStart: .now,
                votingEnd: .now,
                proposals: []
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
        case spendAuthSignatureExtracted(Data, Data)
        case spendAuthSignatureExtractionFailed(String)
        case keystoneBundleAdvance
        case keystoneBundleSignatureStored(State.KeystoneBundleSignature, bundleIndex: UInt32, bundleCount: UInt32)
        case keystoneAllBundlesSigned
        case skipRemainingKeystoneBundles
        case skipBundlesAlert(PresentationAction<Action>)
        case skipRemainingKeystoneBundlesConfirmed
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
        case dismissVoteError
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
                state.screenStack = [.loading]
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
                state.isDelegationProofInFlight = false
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
                        logger.error("Failed to refresh rounds list: \(error)")
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
                    logger.error("Service config fetch failed: \(error)")
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
                    logger.info("Fetched \(allRounds.count) rounds")
                    for round in allRounds {
                        logger.debug(
                            "round=\(round.voteRoundId.hexString.prefix(16))... status=\(round.status.rawValue) snapshot=\(round.snapshotHeight)"
                        )
                    }

                    await send(.allRoundsLoaded(allRounds))
                } catch: { error, send in
                    logger.error("Initialization failed: \(error)")
                    await send(.initializeFailed(error.localizedDescription))
                }

            case .startActiveRoundPipeline:
                guard let session = state.activeSession, session.status == .active else { return .none }
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                let snapshotHeight = session.snapshotHeight
                let roundId = session.voteRoundId.hexString
                // Capture account identity for filtering notes to the selected account
                let accountSeedFingerprint: [UInt8]? = state.selectedWalletAccount?.seedFingerprint
                let accountIndex: UInt32? = state.selectedWalletAccount?.zip32AccountIndex.map { UInt32($0.index) }
                return .run { [votingCrypto, mnemonic, walletStorage, sdkSynchronizer] send in
                    // Check wallet sync progress before querying notes
                    let walletScannedHeight = UInt64(sdkSynchronizer.latestState().latestBlockHeight)
                    if walletScannedHeight < snapshotHeight {
                        logger.info("Wallet scanned to \(walletScannedHeight), snapshot at \(snapshotHeight) — not synced yet")
                        await send(.walletNotSynced(scannedHeight: walletScannedHeight, snapshotHeight: snapshotHeight))
                        return
                    }

                    let notes = try await votingCrypto.getWalletNotes(
                        walletDbPath,
                        snapshotHeight,
                        networkId,
                        accountSeedFingerprint,
                        accountIndex
                    )
                    let totalWeight = notes.reduce(UInt64(0)) { $0 + $1.value }
                    logger.info("Loaded \(notes.count) notes at height \(snapshotHeight), total weight: \(totalWeight)")
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
                        logger.debug("Hotkey address: \(hotkey.address)")
                        await send(.hotkeyLoaded(hotkey.address))
                    } catch {
                        logger.error("Failed to generate hotkey: \(error)")
                    }
                } catch: { error, send in
                    logger.error("Active round pipeline failed: \(error)")
                    await send(.initializeFailed(error.localizedDescription))
                }
                .cancellable(id: cancelPipelineId, cancelInFlight: true)

            case .activeSessionLoaded(let session):
                state.activeSession = session
                state.roundId = session.voteRoundId.hexString
                state.votingRound = sessionBackedRound(from: session, title: state.votingRound.title, fallback: state.votingRound)
                reconcileProposalState(&state)
                let roundPrefix = session.voteRoundId.hexString.prefix(16)
                logger.info("activeSessionLoaded: status=\(session.status.rawValue) round=\(roundPrefix)... proposals=\(session.proposals.count)")
                return .none

            case .noActiveRound:
                state.activeSession = nil
                state.screenStack = [.noRounds]
                return .none

            case let .votingWeightLoaded(weight, notes):
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
                    let dropped = bundleResult.droppedCount
                    logger.info("Smart bundling: dropped \(dropped) notes in sub-threshold bundles (eligible: \(eligibleWeight) of \(weight) total)")
                }
                if eligibleWeight < 12_500_000 {
                    state.ineligibilityReason = .balanceTooLow
                    state.screenStack = [.ineligible]
                    return .none
                }
                // Show proposals immediately while witnesses load in the background.
                // For Keystone users that haven't authorized yet, go straight to the
                // delegation signing screen to avoid a brief flash of the proposal list.
                // Don't set delegationProofStatus here — verifyWitnesses will set it
                // only for fresh rounds, avoiding a brief flash for cached rounds.
                if state.isKeystoneUser {
                    state.screenStack = [.delegationSigning]
                } else {
                    state.screenStack = [.proposalList]
                }
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
                logger.error("Initialization error: \(error)")
                state.screenStack = [.error(VotingErrorMapper.userFriendlyMessage(from: error))]
                return .none

            case let .walletNotSynced(scannedHeight, snapshotHeight):
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
                    logger.error("Status polling error: \(error)")
                }
                .cancellable(id: cancelStatusPollingId, cancelInFlight: true)

            case let .roundStatusUpdated(polledRoundId, newStatus):
                guard let session = state.activeSession else { return .none }

                // Guard against stale poll responses from a previously viewed
                // round arriving after the user navigated to a different round.
                // TCA effect cancellation is cooperative, so a queued action
                // from the old poll can slip through.
                guard polledRoundId == session.voteRoundId else {
                    let polledPrefix = polledRoundId.hexString.prefix(16)
                    let activePrefix = session.voteRoundId.hexString.prefix(16)
                    logger.debug("roundStatusUpdated: ignoring stale poll for \(polledPrefix)..., active round is \(activePrefix)...")
                    return .none
                }

                // Only react to actual transitions
                logger.info("roundStatusUpdated: old=\(session.status.rawValue) new=\(newStatus.rawValue)")
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
                    logger.error("Failed to fetch tally results: \(error)")
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
                        await send(.shareConfirmationsUpdated(proposalId, min(totalShares, 5)))
                    }
                } catch: { error, _ in
                    logger.error("Share confirmation polling error: \(error)")
                }
                .cancellable(id: cancelSharePollingId, cancelInFlight: true)

            case let .shareConfirmationsUpdated(proposalId, count):
                state.shareConfirmations[proposalId] = count
                if count >= 5 {
                    state.isPollingShareConfirmations = false
                    return .cancel(id: cancelSharePollingId)
                }
                return .none

            // MARK: - Witness Verification

            case .verifyWitnesses:
                guard let activeSession = state.activeSession else {
                    state.witnessStatus = .failed(VotingErrorMapper.userFriendlyMessage(from: "missing active session"))
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
                        let emptyTiming = Voting.State.WitnessTiming(
                            treeStateFetchMs: 0,
                            witnessGenerationMs: 0,
                            verificationMs: 0
                        )
                        await send(.witnessVerificationCompleted([], [], emptyTiming, 0))
                        return
                    }

                    // Setup bundles (value-aware split into groups of up to 5)
                    let setupResult = try await votingCrypto.setupBundles(roundId, notes)
                    let bundleCount = setupResult.bundleCount
                    logger.info("Setup \(bundleCount) bundle(s) for \(notes.count) notes (eligible weight: \(setupResult.eligibleWeight))")

                    // Phase 1: Fetch tree state from lightwalletd
                    let fetchStart = ContinuousClock.now
                    let treeStateBytes = try await sdkSynchronizer.getTreeState(snapshotHeight)
                    try await votingCrypto.storeTreeState(roundId, treeStateBytes)
                    let fetchEnd = ContinuousClock.now
                    let fetchMs = UInt64(fetchStart.duration(to: fetchEnd).components.seconds * 1000)
                        + UInt64(fetchStart.duration(to: fetchEnd).components.attoseconds / 1_000_000_000_000_000)
                    logger.debug("Tree state fetch: \(fetchMs)ms")

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
                    let genEnd = ContinuousClock.now
                    let genMs = UInt64(fetchEnd.duration(to: genEnd).components.seconds * 1000)
                        + UInt64(fetchEnd.duration(to: genEnd).components.attoseconds / 1_000_000_000_000_000)
                    logger.debug("Witness generation: \(genMs)ms (\(allWitnesses.count) notes)")

                    // Phase 3: Verify each witness on Swift side for UI display
                    let sortedNotes = noteChunks.flatMap { $0 }
                    var results: [Voting.State.NoteWitnessResult] = []
                    for (idx, witness) in allWitnesses.enumerated() {
                        let verified = (try? await votingCrypto.verifyWitness(witness)) ?? false
                        let note = sortedNotes[idx]
                        results.append(.init(position: note.position, value: note.value, verified: verified))
                        logger.debug("Note pos=\(note.position) value=\(note.value) verified=\(verified)")
                    }
                    let verifyEnd = ContinuousClock.now
                    let verifyMs = UInt64(genEnd.duration(to: verifyEnd).components.seconds * 1000)
                        + UInt64(genEnd.duration(to: verifyEnd).components.attoseconds / 1_000_000_000_000_000)
                    logger.debug("Swift verification: \(verifyMs)ms")
                    logger.info("Total witness pipeline: \(fetchMs + genMs + verifyMs)ms")

                    let timing = Voting.State.WitnessTiming(
                        treeStateFetchMs: fetchMs,
                        witnessGenerationMs: genMs,
                        verificationMs: verifyMs
                    )
                    await send(.witnessVerificationCompleted(results, allWitnesses, timing, bundleCount))
                } catch: { error, send in
                    logger.error("Witness verification failed: \(error)")
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

            case let .witnessVerificationCompleted(results, witnesses, timing, bundleCount):
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
                let message = VotingErrorMapper.userFriendlyMessage(from: error)
                state.witnessStatus = .failed(message)
                state.delegationProofStatus = .failed(message)
                state.isDelegationProofInFlight = false
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
                            logger.error("Failed to restore bundle count: \(error)")
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
                logger.debug("DB state: phase=\(String(describing: dbState.roundState.phase)), \(dbState.votes.count) votes")
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
                state.keystoneBundleSignatures = []
                return .send(.dismissFlow)

            case .retryKeystoneSigning:

                state.pendingGovernancePczt = nil
                state.pendingUnsignedDelegationPczt = nil
                state.keystoneSigningStatus = .idle
                state.currentKeystoneBundleIndex = 0
                state.isDelegationProofInFlight = false
                state.keystoneBundleSignatures = []
                return .send(.startDelegationProof)

            // MARK: - Background ZKP Delegation

            case .startDelegationProof:
                guard !state.isDelegationProofInFlight && state.delegationProofStatus != .complete else {
                    return .none
                }
                state.isDelegationProofInFlight = true
                guard let activeSession = state.activeSession else {
                    return .send(.delegationProofFailed(
                        VotingFlowError.missingActiveSession.localizedDescription
                    ))
                }
                let keystoneMetadata: (seedFingerprint: Data, accountIndex: UInt32)?
                if state.isKeystoneUser {
                    guard
                        let account = state.selectedWalletAccount,
                        let zip32AccountIndex = account.zip32AccountIndex
                    else {
                        return .send(.delegationProofFailed(
                            VotingFlowError.missingSigningAccount.localizedDescription
                        ))
                    }
                    guard
                        let seedFingerprint = account.seedFingerprint,
                        seedFingerprint.count == 32
                    else {
                        return .send(.delegationProofFailed(
                            VotingFlowError.missingSigningAccount.localizedDescription
                        ))
                    }
                    keystoneMetadata = (Data(seedFingerprint), UInt32(zip32AccountIndex.index))
                } else {
                    keystoneMetadata = nil
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
                let accountIndex: UInt32 = keystoneMetadata?.accountIndex ?? 0
                let keystoneSeedFingerprint = keystoneMetadata?.seedFingerprint
                let isKeystoneUser = state.isKeystoneUser
                let roundName = state.votingRound.title
                // PIR server URL from resolved service config
                let pirServerUrl = state.serviceConfig?.pirServers.first?.url ?? "https://46-101-255-48.sslip.io/nullifier"
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
                            // Extract Orchard FVK from the note's UFVK so the PCZT uses
                            // Keystone's ak (matching what the ZKP prover derives from the
                            // note's ufvk_str). Without this, rk in the PCZT would be
                            // derived from the app's ak, causing a mismatch (Bug 3 fix).
                            let orchardFvk = try votingCrypto.extractOrchardFvkFromUfvk(
                                bundleNotes[0].ufvkStr, networkId
                            )
                            logger.info("Keystone: preparing PCZT for bundle \(keystoneBundleIndex + 1)/\(bundleCount)")
                            let govPczt = try await votingCrypto.buildGovernancePczt(
                                roundId,
                                keystoneBundleIndex,
                                bundleNotes,
                                senderSeed,
                                hotkeySeed,
                                networkId,
                                accountIndex,
                                roundName,
                                orchardFvk,
                                keystoneSeedFingerprint
                            )
                            let redactedPczt = try await sdkSynchronizer
                                .redactPCZTForSigner(govPczt.pcztBytes)
                            await send(.keystoneSigningPrepared(govPczt, redactedPczt))
                            return
                        }

                        // Non-Keystone path: iterate bundles, build PCZT + prove delegation for each.
                        // buildGovernancePczt stores delegation data (alpha, padded note secrets,
                        // ZIP-244 sighash) in the DB, then buildAndProveDelegation uses it for
                        // the ZKP with deterministic randomness (ZCA-74 fix).
                        let noteChunks = cachedNotes.smartBundles().bundles
                        let bundleCount = UInt32(noteChunks.count)

                        for bundleIndex: UInt32 in 0..<bundleCount {
                            let bundleNotes = noteChunks[Int(bundleIndex)]
                            logger.info("Delegation bundle \(bundleIndex + 1)/\(bundleCount) (\(bundleNotes.count) notes)")

                            // Build governance PCZT — stores delegation data + ZIP-244 sighash
                            // in the DB. Same path as Keystone, but no FVK override needed
                            // since the app holds the spending key.
                            _ = try await votingCrypto.buildGovernancePczt(
                                roundId,
                                bundleIndex,
                                bundleNotes,
                                senderSeed,
                                hotkeySeed,
                                networkId,
                                accountIndex,
                                roundName,
                                nil, // no orchardFvkOverride
                                nil // no keystoneSeedFingerprint
                            )

                            for try await event in votingCrypto.buildAndProveDelegation(
                                roundId,
                                bundleIndex,
                                bundleNotes,
                                senderSeed,
                                hotkeySeed,
                                networkId,
                                accountIndex,
                                pirServerUrl
                            ) {
                                switch event {
                                case .progress(let progress):
                                    // Scale progress: each bundle contributes 1/bundleCount of total
                                    let overallProgress = (Double(bundleIndex) + progress) / Double(bundleCount)
                                    logger.debug("ZKP #1 bundle \(bundleIndex) progress: \(Int(progress * 100))%")
                                    await send(.delegationProofProgress(overallProgress))
                                case .completed(let proof):
                                    logger.info("ZKP #1 bundle \(bundleIndex) COMPLETE — proof size: \(proof.count) bytes")
                                }
                            }

                            // Submit delegation TX for this bundle
                            let registration = try await votingCrypto.getDelegationSubmission(
                                roundId, bundleIndex, senderSeed, networkId, accountIndex
                            )
                            let preTree = try await votingAPI.fetchLatestCommitmentTree()
                            let delegTxResult = try await votingAPI.submitDelegation(registration)
                            logger.info("Delegation TX \(bundleIndex) submitted: \(delegTxResult.txHash)")

                            // Poll until the delegation TX lands and the tree grows
                            let postTree = try await votingAPI.awaitCommitmentTreeGrowth(preTree.nextIndex, 90)
                            let vanPosition = UInt32(postTree.nextIndex) - 1
                            try await votingCrypto.storeVanPosition(roundId, bundleIndex, vanPosition)
                            logger.debug("VAN position stored for bundle \(bundleIndex): \(vanPosition)")
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

            case let .keystoneSigningPrepared(govPczt, unsignedPczt):
                state.pendingGovernancePczt = govPczt

                state.pendingUnsignedDelegationPczt = unsignedPczt
                state.keystoneSigningStatus = .awaitingSignature
                return .none

            case .keystoneSigningFailed(let error):
                state.keystoneSigningStatus = .failed(VotingErrorMapper.userFriendlyMessage(from: error))
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
                    await send(.spendAuthSignatureExtracted(spendAuthSig, signedPczt))
                } catch: { error, send in
                    await send(.spendAuthSignatureExtractionFailed(error.localizedDescription))
                }

            case .keystoneScan(.presented(.cancelTapped)),
                .keystoneScan(.dismiss):
                state.keystoneScan = nil
                return .none

            case .keystoneScan:
                return .none

            case let .spendAuthSignatureExtracted(keystoneSig, signedPczt):
                guard let rk = state.pendingGovernancePczt?.rk else { // swiftlint:disable:this identifier_name
                    return .send(.delegationProofFailed(
                        VotingFlowError.missingPendingUnsignedPczt.localizedDescription
                    ))
                }

                // Extract ZIP-244 sighash from the signed PCZT synchronously in a
                // lightweight .run so we can store it alongside the sig.
                let bundleCount = state.bundleCount
                let currentIndex = state.currentKeystoneBundleIndex
                return .run { [votingCrypto] send in
                    let keystoneSighash = try votingCrypto.extractPcztSighash(signedPczt)
                    // Store signature for this bundle
                    await send(.keystoneBundleSignatureStored(
                        .init(sig: keystoneSig, sighash: keystoneSighash, rk: rk),
                        bundleIndex: currentIndex,
                        bundleCount: bundleCount
                    ))
                } catch: { error, send in
                    await send(.spendAuthSignatureExtractionFailed(error.localizedDescription))
                }

            case let .keystoneBundleSignatureStored(signature, bundleIndex, bundleCount):
                state.keystoneBundleSignatures.append(signature)
                state.pendingGovernancePczt = nil
                state.pendingUnsignedDelegationPczt = nil

                if bundleIndex + 1 < bundleCount {
                    // More bundles to sign — advance index and reset to idle.
                    // User taps "Confirm with Keystone" to start the next bundle.
                    state.currentKeystoneBundleIndex += 1
                    state.isDelegationProofInFlight = false
                    state.keystoneSigningStatus = .idle
                    return .none
                } else {
                    // All bundles signed — navigate to proposal list and start batch proving
                    state.keystoneSigningStatus = .idle
                    state.screenStack = [.proposalList]
                    state.delegationProofStatus = .generating(progress: 0)
                    return .send(.keystoneAllBundlesSigned)
                }

            case .keystoneAllBundlesSigned:
                guard let activeSession = state.activeSession else {
                    return .send(.delegationProofFailed(
                        VotingFlowError.missingActiveSession.localizedDescription
                    ))
                }

                let roundId = activeSession.voteRoundId.hexString
                let cachedNotes = state.walletNotes
                let network = zcashSDKEnvironment.network
                let walletDbPath = databaseFiles.dataDbURLFor(network).path
                let networkId: UInt32 = network.networkType == .mainnet ? 0 : 1
                let accountIndex: UInt32 = state.selectedWalletAccount.flatMap(\.zip32AccountIndex).map { UInt32($0.index) } ?? 0
                let pirServerUrl = state.serviceConfig?.pirServers.first?.url ?? "https://46-101-255-48.sslip.io/nullifier"
                let storedSignatures = state.keystoneBundleSignatures
                let signedCount = storedSignatures.count

                return .run { [votingCrypto, votingAPI, mnemonic, walletStorage] send in
                    let senderPhrase = try walletStorage.exportWallet().seedPhrase.value()
                    let senderSeed = try mnemonic.toSeed(senderPhrase)
                    let hotkeyPhrase = try walletStorage.exportVotingHotkey().seedPhrase.value()
                    let hotkeySeed = try mnemonic.toSeed(hotkeyPhrase)
                    let noteChunks = cachedNotes.smartBundles().bundles

                    for (bundleIndex, sig) in storedSignatures.enumerated() {
                        let bundleNotes = noteChunks[bundleIndex]
                        let bundleIdx = UInt32(bundleIndex)
                        logger.info("Keystone batch: proving bundle \(bundleIndex + 1)/\(signedCount)")

                        // buildGovernancePczt already stored the delegation data — just prove.
                        for try await event in votingCrypto.buildAndProveDelegation(
                            roundId,
                            bundleIdx,
                            bundleNotes,
                            senderSeed,
                            hotkeySeed,
                            networkId,
                            accountIndex,
                            pirServerUrl
                        ) {
                            switch event {
                            case .progress(let progress):
                                let overallProgress = (Double(bundleIndex) + progress) / Double(signedCount)
                                logger.debug("ZKP #1 bundle \(bundleIdx) progress: \(Int(progress * 100))%")
                                await send(.delegationProofProgress(overallProgress))
                            case .completed(let proof):
                                logger.info("ZKP #1 bundle \(bundleIdx) COMPLETE — proof size: \(proof.count) bytes")
                            }
                        }

                        // Submit delegation TX using the stored Keystone signature
                        let registration = try await votingCrypto.getDelegationSubmissionWithKeystoneSig(
                            roundId, bundleIdx, sig.sig, sig.sighash
                        )
                        if registration.rk != sig.rk ||
                            registration.spendAuthSig != sig.sig ||
                            registration.sighash != sig.sighash {
                            throw VotingFlowError.invalidDelegationSignature
                        }
                        logger.debug(
                            """
                            Keystone delegation tuple \
                            rk=\(Data(registration.rk.prefix(8)).hexString) \
                            sighash=\(Data(sig.sighash.prefix(8)).hexString) \
                            sig=\(Data(sig.sig.prefix(8)).hexString)
                            """
                        )
                        let preTree = try await votingAPI.fetchLatestCommitmentTree()
                        let delegTxResult = try await votingAPI.submitDelegation(registration)
                        logger.info("Delegation TX \(bundleIdx) submitted: \(delegTxResult.txHash)")

                        // Poll until the delegation TX lands and the tree grows
                        let postTree = try await votingAPI.awaitCommitmentTreeGrowth(preTree.nextIndex, 90)
                        let vanPosition = UInt32(postTree.nextIndex) - 1
                        try await votingCrypto.storeVanPosition(roundId, bundleIdx, vanPosition)
                        logger.debug("VAN position stored for bundle \(bundleIdx): \(vanPosition)")
                    }

                    await send(.delegationProofCompleted)
                } catch: { error, send in
                    await send(.delegationProofFailed(error.localizedDescription))
                }

            case .skipRemainingKeystoneBundles:
                // Show confirmation alert with locked-in / giving-up amounts.
                let signedCount = state.keystoneBundleSignatures.count
                guard signedCount > 0 else { return .none }
                state.skipBundlesAlert = .confirmSkip(
                    lockedIn: state.signedBundlesZECString,
                    givingUp: state.skippedBundlesZECString
                )
                return .none

            case .skipBundlesAlert(.presented(.skipRemainingKeystoneBundlesConfirmed)):
                state.skipBundlesAlert = nil
                return .send(.skipRemainingKeystoneBundlesConfirmed)

            case .skipBundlesAlert(.dismiss):
                state.skipBundlesAlert = nil
                return .none

            case .skipBundlesAlert:
                return .none

            case .skipRemainingKeystoneBundlesConfirmed:
                // User confirmed skip — proceed with only the signed bundles.
                let signedCount = UInt32(state.keystoneBundleSignatures.count)
                guard signedCount > 0 else { return .none }
                state.bundleCount = signedCount

                // Recalculate votingWeight to reflect only signed bundles' weight
                let bundles = state.walletNotes.smartBundles().bundles
                let signedWeight = state.keystoneBundleSignatures.indices.reduce(UInt64(0)) { total, i in
                    guard i < bundles.count else { return total }
                    return total + bundles[i].reduce(UInt64(0)) { $0 + $1.value }
                }
                state.votingWeight = signedWeight

                state.pendingGovernancePczt = nil
                state.pendingUnsignedDelegationPczt = nil
                state.keystoneSigningStatus = .idle
                state.screenStack = [.proposalList]
                state.delegationProofStatus = .generating(progress: 0)

                // Delete skipped bundles from DB so proof_generated reflects reality
                let roundId = state.roundId
                return .run { [votingCrypto] send in
                    try await votingCrypto.deleteSkippedBundles(roundId, signedCount)
                    await send(.keystoneAllBundlesSigned)
                } catch: { error, send in
                    await send(.delegationProofFailed(error.localizedDescription))
                }

            case .keystoneBundleAdvance:
                // Legacy — no longer used; signing loop is handled by keystoneBundleSignatureStored.
                return .none

            case .spendAuthSignatureExtractionFailed(let error):
                state.keystoneSigningStatus = .failed(VotingErrorMapper.userFriendlyMessage(from: error))
                return .none

            case .delegationProofProgress(let progress):
                state.delegationProofStatus = .generating(progress: progress)
                return .none

            case .delegationProofCompleted:
                state.delegationProofStatus = .complete
                state.isDelegationProofInFlight = false
                state.currentKeystoneBundleIndex = 0
                state.keystoneBundleSignatures = []
                return .none

            case .delegationProofFailed(let error):
                state.currentKeystoneBundleIndex = 0
                state.keystoneBundleSignatures = []
                let userMessage: String
                if error.contains("total_weight must yield at least 1 ballot") {
                    let weightStr = Zatoshi(Int64(state.votingWeight)).decimalString()
                    let requiredStr = Zatoshi(12_500_000).decimalString()
                    userMessage = """
                        Your shielded balance at the snapshot (\(weightStr) ZEC) \
                        is below the minimum required to vote (\(requiredStr) ZEC).
                        """
                } else {
                    userMessage = VotingErrorMapper.userFriendlyMessage(from: error)
                }
                state.delegationProofStatus = .failed(userMessage)
                state.isDelegationProofInFlight = false
                return .none

            // MARK: - Proposal List

            case .proposalTapped(let id):
                state.selectedProposalId = id
                state.screenStack.append(.proposalDetail(id: id))
                return .none

            // MARK: - Proposal Detail

            case let .castVote(proposalId, choice):
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
                            logger.debug("Vote bundle \(bundleIndex + 1)/\(bundleCount) already submitted, skipping")
                            continue
                        }
                        logger.info("Vote bundle \(bundleIndex + 1)/\(bundleCount) for proposal \(proposalId)")

                        // Update progress per-bundle so the UI shows which bundle is being processed
                        await send(.voteSubmissionBundleStarted(bundleIndex))

                        // Sync vote commitment tree from chain and generate VAN witness.
                        // Requires storeVanPosition to have been called after delegation TX.
                        let anchorHeight = try await votingCrypto.syncVoteTree(roundId, chainNodeUrl)
                        let vanWitness = try await votingCrypto.generateVanWitness(roundId, bundleIndex, anchorHeight)
                        logger.debug("VAN witness: position=\(vanWitness.position), anchor=\(vanWitness.anchorHeight)")

                        // Build vote commitment + ZKP #2 (stored in DB).
                        // The builder internally decomposes weight, encrypts shares under EA pk,
                        // and returns encrypted shares in the bundle.
                        var builtBundle: VoteCommitmentBundle?
                        for try await event in votingCrypto.buildVoteCommitment(
                            roundId,
                            bundleIndex,
                            hotkeySeed,
                            networkId,
                            proposalId,
                            choice,
                            numOptions,
                            vanWitness.authPath,
                            vanWitness.position,
                            vanWitness.anchorHeight
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
                        let txHash = txResult.txHash
                        await send(.voteCommitmentSubmitted(txHash))

                        // Wait for the cast-vote TX to land and read the new tree position.
                        // The chain appends vote_authority_note_new first, then vote_commitment,
                        // so the new VAN is at nextIndex-2 and the VC is at nextIndex-1.
                        // If the tree doesn't grow in time, fall back to TX hash polling.
                        let postVCTree: CommitmentTreeState
                        do {
                            postVCTree = try await votingAPI.awaitCommitmentTreeGrowth(preVCTree.nextIndex, 90)
                        } catch {
                            logger.warning("Tree growth timeout, falling back to TX confirmation polling: \(error)")
                            guard !txHash.isEmpty else { throw error }

                            // Poll TX status for up to 30s to verify it actually landed
                            let txDeadline = Date().addingTimeInterval(30)
                            var confirmation: TxConfirmation?
                            while Date() < txDeadline {
                                if let result = try await votingAPI.checkTxConfirmed(txHash) {
                                    confirmation = result
                                    break
                                }
                                try await Task.sleep(for: .seconds(2))
                            }
                            guard let confirmation else { throw error }
                            guard confirmation.code == 0 else {
                                throw VotingFlowError.voteCommitmentTxFailed(code: confirmation.code)
                            }

                            // TX landed — reset stale tree client and re-fetch tree state
                            logger.info("TX confirmed at height \(confirmation.height), resetting tree client")
                            try await votingCrypto.resetTreeClient()
                            postVCTree = try await votingAPI.fetchLatestCommitmentTree()
                        }

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
                                logger.warning("delegateShares attempt \(attempt)/3 failed: \(error)")
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
                    logger.error("vote submission failed: \(error)")
                    await send(.voteSubmissionFailed(proposalId: proposalId, error: error.localizedDescription))
                }

            case .voteCommitmentBuilt(let bundle):
                state.lastVoteCommitmentBundle = bundle
                return .none

            case .voteCommitmentSubmitted(let txHash):
                state.lastVoteCommitmentTxHash = txHash
                return .none

            case let .voteSubmissionFailed(proposalId, error):
                state.isSubmittingVote = false
                state.submittingProposalId = nil
                state.voteSubmissionStep = nil
                state.voteSubmissionError = VotingErrorMapper.userFriendlyMessage(from: error)
                state.currentVoteBundleIndex = nil
                // Remove the optimistic vote since it didn't land on chain
                state.votes.removeValue(forKey: proposalId)
                return .none

            case .dismissVoteError:
                state.voteSubmissionError = nil
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
    /// 2. Fill bundles sequentially to capacity (5 notes each)
    /// 3. Drop bundles with total < ballotDivisor
    /// 4. Re-sort notes within each surviving bundle by position
    /// 5. Sort surviving bundles by total value DESC (min position as tiebreaker)
    func smartBundles() -> BundleResult {
        guard !isEmpty else {
            return BundleResult(bundles: [], eligibleWeight: 0, droppedCount: 0)
        }

        // Step 1: Sort by value DESC, then position ASC
        let sorted = self.sorted { lhs, rhs in
            if lhs.value != rhs.value { return lhs.value > rhs.value }
            return lhs.position < rhs.position
        }

        // Step 2: Fill bundles sequentially to capacity (5 notes each)
        var bundleNotes: [[NoteInfo]] = []
        var bundleTotals: [UInt64] = []

        for note in sorted {
            if bundleNotes.isEmpty || (bundleNotes.last?.count ?? 0) >= 5 {
                bundleNotes.append([])
                bundleTotals.append(0)
            }
            let last = bundleNotes.count - 1
            bundleTotals[last] += note.value
            bundleNotes[last].append(note)
        }

        // Step 3: Drop bundles with total < ballotDivisor
        let numBundles = bundleNotes.count
        var surviving: [(total: UInt64, notes: [NoteInfo])] = []
        var eligibleWeight: UInt64 = 0
        var survivingNoteCount = 0

        for i in 0..<numBundles where bundleTotals[i] >= Self.ballotDivisor {
            surviving.append((bundleTotals[i], bundleNotes[i]))
            // Quantize per bundle: VAN weight = floor(total / ballotDivisor) * ballotDivisor
            eligibleWeight += (bundleTotals[i] / Self.ballotDivisor) * Self.ballotDivisor
            survivingNoteCount += bundleNotes[i].count
        }
        let droppedCount = count - survivingNoteCount

        // Step 5: Re-sort notes within each surviving bundle by position
        for i in 0..<surviving.count {
            surviving[i].notes.sort { $0.position < $1.position }
        }

        // Step 6: Sort surviving bundles by total value DESC (min position as tiebreaker).
        // This ensures bundle 0 is always the most valuable, enabling users to skip
        // low-value trailing bundles during Keystone signing.
        surviving.sort { lhs, rhs in
            if lhs.total != rhs.total { return lhs.total > rhs.total }
            return (lhs.notes.first?.position ?? .max) < (rhs.notes.first?.position ?? .max)
        }

        return BundleResult(bundles: surviving.map(\.notes), eligibleWeight: eligibleWeight, droppedCount: droppedCount)
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

// MARK: - Skip Bundles Alert

extension AlertState where Action == Voting.Action {
    static func confirmSkip(lockedIn: String, givingUp: String) -> AlertState {
        AlertState {
            TextState("Skip Remaining Bundles?")
        } actions: {
            ButtonState(role: .destructive, action: .skipRemainingKeystoneBundlesConfirmed) {
                TextState("Skip")
            }
            ButtonState(role: .cancel, action: .skipBundlesAlert(.dismiss)) {
                TextState("Cancel")
            }
        } message: {
            TextState("You will lock in \(lockedIn) ZEC from signed bundles and give up \(givingUp) ZEC. This cannot be undone for this round.")
        }
    }
}
