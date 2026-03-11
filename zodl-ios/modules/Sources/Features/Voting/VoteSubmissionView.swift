import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents
import VotingModels

struct VoteCompletionView: View {
    let store: StoreOf<Voting>

    var body: some View {
        WithPerceptionTracking {
            VStack(spacing: 32) {
                Spacer()

                VStack(spacing: 20) {
                    Image(systemName: "checkmark.circle.fill")
                        .font(.system(size: 64))
                        .foregroundStyle(.green)

                    Text("Votes Submitted!")
                        .zFont(.semiBold, size: 22, style: Design.Text.primary)

                    Text("Your \(store.votingWeightZECString) ZEC in eligible funds has been applied to \(store.totalProposals) proposals.")
                        .zFont(.regular, size: 15, style: Design.Text.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 32)

                    ZashiButton("Done", type: .primary) {
                        store.send(.doneTapped)
                    }
                    .padding(.horizontal, 40)
                    .padding(.top, 16)
                }

                Spacer()
            }
            .navigationBarBackButtonHidden(true)
        }
    }
}
