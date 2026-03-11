import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents

struct VotingErrorView: View {
    @Environment(\.colorScheme)
    var colorScheme

    let store: StoreOf<Voting>
    let errorMessage: String

    var body: some View {
        WithPerceptionTracking {
            VStack(spacing: 0) {
                Spacer()

                VStack(spacing: 24) {
                    // Icon
                    ZStack {
                        Circle()
                            .fill(Color.red.opacity(0.12))
                            .frame(width: 72, height: 72)
                        Image(systemName: "exclamationmark.circle")
                            .font(.system(size: 32))
                            .foregroundStyle(.red)
                    }

                    // Title
                    Text("Something Went Wrong")
                        .zFont(.semiBold, size: 22, style: Design.Text.primary)

                    // Description (already mapped to a user-friendly message by VotingErrorMapper)
                    Text(errorMessage)
                        .zFont(.regular, size: 15, style: Design.Text.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 32)
                }

                Spacer()

                // Retry + Dismiss buttons
                VStack(spacing: 12) {
                    ZashiButton("Retry") {
                        store.send(.initialize)
                    }

                    ZashiButton("Dismiss", type: .ghost) {
                        store.send(.dismissFlow)
                    }
                }
                .padding(.horizontal, 24)
                .padding(.bottom, 24)
            }
            .navigationTitle("Governance")
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Design.screenBackground.color(colorScheme))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button {
                        store.send(.dismissFlow)
                    } label: {
                        Image(systemName: "xmark")
                    }
                }
            }
        }
    }
}
