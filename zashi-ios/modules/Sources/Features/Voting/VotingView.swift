import SwiftUI
import ComposableArchitecture

public struct VotingView: View {
    let store: StoreOf<Voting>

    public init(store: StoreOf<Voting>) {
        self.store = store
    }

    public var body: some View {
        WithPerceptionTracking {
            screenView(for: store.currentScreen)
                .id(store.screenStack.count)
                .transition(.move(edge: .trailing))
                .animation(.easeInOut(duration: 0.25), value: store.screenStack.count)
        }
        .navigationBarTitleDisplayMode(.inline)
        .navigationBarBackButtonHidden(true)
    }

    @ViewBuilder
    private func screenView(for screen: Voting.State.Screen) -> some View {
        switch screen {
        case .landing:
            VotingLandingView(store: store)
        case .keystoneSigning:
            KeystoneSigningView(store: store)
        case .proposalList:
            ProposalListView(store: store)
        case .proposalDetail:
            if let proposal = store.selectedProposal {
                ProposalDetailView(store: store, proposal: proposal)
            }
        case .voteReview:
            VoteReviewView(store: store)
        case .voteSubmission, .complete:
            VoteSubmissionView(store: store)
        }
    }
}

// MARK: - Placeholders

extension Voting.State {
    public static let initial = Voting.State()
}

extension StoreOf<Voting> {
    public static let placeholder = StoreOf<Voting>(
        initialState: .initial
    ) {
        Voting()
    }
}

#Preview {
    NavigationStack {
        VotingView(store: .placeholder)
    }
}
