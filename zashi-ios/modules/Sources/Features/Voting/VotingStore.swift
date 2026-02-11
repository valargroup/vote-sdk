import Foundation
import ComposableArchitecture

@Reducer
public struct Voting {
    @ObservableState
    public struct State: Equatable {
        public enum Screen: Equatable {
            case delegationSigning
            case proposalList
            case proposalDetail(id: String)
            case voteReview
            case voteSubmission
            case complete
        }

        public var screenStack: [Screen] = [.delegationSigning]
        public var votingRound: VotingRound
        public var votes: [String: VoteChoice] = [:]
        public var votingWeight: UInt64
        public var isKeystoneUser: Bool

        // Which proposal the bottom bar targets
        public var selectedProposalId: String?

        // ZKP #1 (delegation) — runs in background
        public var delegationProofStatus: ProofStatus = .notStarted

        // Submission
        public var submissionStatus: SubmissionStatus = .idle

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

        public var canSubmitVotes: Bool {
            allVoted && isDelegationReady
        }

        public var nextUnvotedProposalId: String? {
            votingRound.proposals.first { votes[$0.id] == nil }?.id
        }

        public var activeProposalId: String? {
            selectedProposalId ?? nextUnvotedProposalId
        }

        public var selectedProposal: Proposal? {
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
            votingWeight: UInt64 = MockVotingService.votingWeight,
            isKeystoneUser: Bool = false
        ) {
            self.votingRound = votingRound
            self.votingWeight = votingWeight
            self.isKeystoneUser = isKeystoneUser
        }
    }

    public enum Action: Equatable {
        // Navigation
        case dismissFlow
        case goBack

        // Delegation signing
        case delegationApproved
        case delegationRejected

        // Background ZKP delegation
        case startDelegationProof
        case delegationProofProgress(Double)
        case delegationProofCompleted
        case delegationProofFailed(String)

        // Proposal list
        case proposalTapped(String)
        case bottomBarNext
        case bottomBarPrevious

        // Proposal detail
        case castVote(proposalId: String, choice: VoteChoice)
        case advanceAfterVote(nextId: String?)
        case backToList
        case nextProposalDetail
        case previousProposalDetail

        // Vote review
        case reviewVotesTapped
        case editVote(proposalId: String)
        case submitVotesTapped

        // Vote submission
        case submissionProgress(Int, Int)
        case submissionCompleted
        case submissionFailed(String)

        // Complete
        case doneTapped
    }

    public init() {}

    public var body: some Reducer<State, Action> {
        Reduce { state, action in
            switch action {
            // MARK: - Navigation

            case .dismissFlow:
                return .none

            case .goBack:
                if state.screenStack.count > 1 {
                    state.screenStack.removeLast()
                }
                return .none

            // MARK: - Delegation Signing

            case .delegationApproved:
                state.screenStack = [.proposalList]
                return .send(.startDelegationProof)

            case .delegationRejected:
                return .send(.dismissFlow)

            // MARK: - Background ZKP Delegation

            case .startDelegationProof:
                state.delegationProofStatus = .generating(progress: 0)
                return .run { send in
                    for step in 1...8 {
                        try await Task.sleep(for: .milliseconds(500))
                        await send(.delegationProofProgress(Double(step) / 8.0))
                    }
                    await send(.delegationProofCompleted)
                }

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

            case .bottomBarNext:
                let proposals = state.votingRound.proposals
                if let currentId = state.activeProposalId,
                   let idx = proposals.firstIndex(where: { $0.id == currentId }),
                   idx + 1 < proposals.count {
                    state.selectedProposalId = proposals[idx + 1].id
                }
                return .none

            case .bottomBarPrevious:
                let proposals = state.votingRound.proposals
                if let currentId = state.activeProposalId,
                   let idx = proposals.firstIndex(where: { $0.id == currentId }),
                   idx > 0 {
                    state.selectedProposalId = proposals[idx - 1].id
                }
                return .none

            case .proposalTapped(let id):
                state.selectedProposalId = id
                state.screenStack.append(.proposalDetail(id: id))
                return .none

            // MARK: - Proposal Detail

            case .castVote(let proposalId, let choice):
                state.votes[proposalId] = choice

                if case .proposalDetail = state.currentScreen {
                    // Detail view: delay before advancing so user sees their vote land
                    let nextId = nextUnvotedId(after: proposalId, in: state)
                    return .run { send in
                        try await Task.sleep(for: .milliseconds(600))
                        await send(.advanceAfterVote(nextId: nextId))
                    }
                } else {
                    // List view: auto-advance bottom bar to next unvoted
                    if let nextId = nextUnvotedId(after: proposalId, in: state) {
                        state.selectedProposalId = nextId
                    } else {
                        state.selectedProposalId = nil
                    }
                }
                return .none

            case .advanceAfterVote(let nextId):
                if case .proposalDetail = state.currentScreen {
                    if let nextId {
                        state.selectedProposalId = nextId
                        state.screenStack.removeLast()
                        state.screenStack.append(.proposalDetail(id: nextId))
                    } else {
                        state.selectedProposalId = nil
                        state.screenStack.removeLast()
                    }
                }
                return .none

            case .backToList:
                if case .proposalDetail = state.currentScreen {
                    state.screenStack.removeLast()
                }
                return .none

            case .nextProposalDetail:
                if let index = state.detailProposalIndex,
                   index + 1 < state.votingRound.proposals.count {
                    let nextId = state.votingRound.proposals[index + 1].id
                    state.selectedProposalId = nextId
                    state.screenStack.removeLast()
                    state.screenStack.append(.proposalDetail(id: nextId))
                }
                return .none

            case .previousProposalDetail:
                if let index = state.detailProposalIndex, index > 0 {
                    let prevId = state.votingRound.proposals[index - 1].id
                    state.selectedProposalId = prevId
                    state.screenStack.removeLast()
                    state.screenStack.append(.proposalDetail(id: prevId))
                }
                return .none

            // MARK: - Vote Review

            case .reviewVotesTapped:
                state.screenStack.append(.voteReview)
                return .none

            case .editVote(let proposalId):
                state.screenStack.append(.proposalDetail(id: proposalId))
                return .none

            case .submitVotesTapped:
                state.screenStack.append(.voteSubmission)
                state.submissionStatus = .submitting(proposalIndex: 0, total: state.totalProposals)
                return .run { [total = state.totalProposals] send in
                    for index in 0..<total {
                        await send(.submissionProgress(index, total))
                        try await Task.sleep(for: .milliseconds(400))
                    }
                    try await Task.sleep(for: .milliseconds(300))
                    await send(.submissionCompleted)
                }

            // MARK: - Vote Submission

            case .submissionProgress(let index, let total):
                state.submissionStatus = .submitting(proposalIndex: index, total: total)
                return .none

            case .submissionCompleted:
                state.submissionStatus = .complete
                state.screenStack.removeLast()
                state.screenStack.append(.complete)
                return .none

            case .submissionFailed(let error):
                state.submissionStatus = .failed(error)
                return .none

            // MARK: - Complete

            case .doneTapped:
                return .send(.dismissFlow)
            }
        }
    }

    // Find the next unvoted proposal after the given one (wrapping around)
    private func nextUnvotedId(after proposalId: String, in state: State) -> String? {
        let proposals = state.votingRound.proposals
        guard let currentIndex = proposals.firstIndex(where: { $0.id == proposalId }) else { return nil }

        // Look forward first, then wrap
        return proposals[(currentIndex + 1)...].first { state.votes[$0.id] == nil }?.id
            ?? proposals[..<currentIndex].first { state.votes[$0.id] == nil }?.id
    }
}
