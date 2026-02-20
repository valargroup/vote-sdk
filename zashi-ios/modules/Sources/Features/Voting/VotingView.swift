import SwiftUI
import ComposableArchitecture
import Scan
import VotingModels

public struct VotingView: View {
    let store: StoreOf<Voting>

    public init(store: StoreOf<Voting>) {
        self.store = store
    }

    public var body: some View {
        WithPerceptionTracking {
            let screen = store.screenStack.last ?? .proposalList
            screenView(for: screen)
                .id(screenId(screen))
                .animation(.easeInOut(duration: 0.3), value: store.selectedProposal?.id)
        }
        .navigationBarTitleDisplayMode(.inline)
        .navigationBarBackButtonHidden(true)
        .onAppear {
            store.send(.initialize)
        }
        .sheet(
            store: store.scope(state: \.$keystoneScan, action: \.keystoneScan)
        ) { scanStore in
            ScanView(store: scanStore, popoverRatio: 1.075)
        }
    }

    private func screenId(_ screen: Voting.State.Screen) -> String {
        switch screen {
        case .loading: return "loading"
        case .roundsList: return "roundsList"
        case .delegationSigning: return "delegationSigning"
        case .proposalList: return "proposalList"
        case .proposalDetail(let id): return "detail-\(id)"
        case .complete: return "complete"
        case .ineligible: return "ineligible"
        case .tallying: return "tallying"
        case .results: return "results"
        case .error: return "error"
        case .walletSyncing: return "walletSyncing"
        }
    }

    @ViewBuilder
    private func screenView(for screen: Voting.State.Screen) -> some View {
        switch screen {
        case .loading:
            ProgressView()
        case .roundsList:
            RoundsListView(store: store)
        case .delegationSigning:
            DelegationSigningView(store: store)
        case .proposalList:
            ProposalListView(store: store)
        case .proposalDetail:
            if let proposal = store.selectedProposal {
                ProposalDetailView(store: store, proposal: proposal)
                    .id(proposal.id)
                    .transition(.push(from: .trailing))
            }
        case .complete:
            VoteCompletionView(store: store)
        case .ineligible:
            IneligibleView(store: store)
        case .tallying:
            TallyingView(store: store)
        case .results:
            ResultsView(store: store)
        case .error(let message):
            VotingErrorView(store: store, errorMessage: message)
        case .walletSyncing:
            WalletSyncingView(store: store)
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
