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

    // swiftlint:disable:next cyclomatic_complexity
    private func screenId(_ screen: Voting.State.Screen) -> String {
        switch screen {
        case .loading: return "loading"
        case .noRounds: return "noRounds"
        case .roundsList: return "roundsList"  // Dead — kept for exhaustive switch
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
    private func screenView( // swiftlint:disable:this cyclomatic_complexity
        for screen: Voting.State.Screen
    ) -> some View {
        switch screen {
        case .loading:
            ProgressView()
        case .noRounds:
            NoRoundsView(store: store)
        case .roundsList:
            NoRoundsView(store: store)  // Dead — kept for exhaustive switch
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

// MARK: - No Rounds

struct NoRoundsView: View {
    let store: StoreOf<Voting>

    var body: some View {
        VStack(spacing: 12) {
            Spacer()
            Image(systemName: "rectangle.slash")
                .font(.system(size: 28))
                .foregroundStyle(.secondary)
            Text("No Voting Rounds")
                .font(.headline)
            Text("There are no voting rounds available right now.")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)
            Spacer()
        }
        .navigationTitle("Governance")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .navigationBarLeading) {
                Button { store.send(.dismissFlow) } label: {
                    Image(systemName: "xmark")
                }
            }
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
