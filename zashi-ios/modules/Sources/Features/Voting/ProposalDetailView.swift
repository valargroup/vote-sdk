import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents
import VotingModels

struct ProposalDetailView: View {
    @Environment(\.colorScheme) var colorScheme

    let store: StoreOf<Voting>
    let proposal: Proposal

    private let impactFeedback = UIImpactFeedbackGenerator(style: .light)

    var body: some View {
        WithPerceptionTracking {
            ZStack {
                VStack(spacing: 0) {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 20) {
                            // Header
                            VStack(alignment: .leading, spacing: 8) {
                                if let zip = proposal.zipNumber {
                                    ZIPBadge(zipNumber: zip)
                                }

                                Text(proposal.title)
                                    .zFont(.semiBold, size: 22, style: Design.Text.primary)

                                Text(proposal.description)
                                    .zFont(.regular, size: 15, style: Design.Text.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                            }

                            // Forum link
                            if let url = proposal.forumURL {
                                Link(destination: url) {
                                    HStack(spacing: 6) {
                                        Image(systemName: "bubble.left.and.text.bubble.right")
                                            .font(.caption)
                                        Text("View Forum Discussion")
                                            .zFont(.medium, size: 14, style: Design.Text.link)
                                    }
                                }
                            }

                            Spacer().frame(height: 8)

                            // Vote section
                            voteSection()

                            Spacer()
                        }
                        .padding(.horizontal, 24)
                        .padding(.top, 16)
                    }

                    // Bottom prev/next navigation
                    proposalNavigationBar()
                }

                // Confirmation overlay
                confirmationOverlay()
            }
            .navigationTitle(positionLabel)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button {
                        store.send(.backToList)
                    } label: {
                        HStack(spacing: 4) {
                            Image(systemName: "chevron.left")
                            Text("List")
                                .font(.system(size: 16))
                        }
                    }
                }
            }
        }
    }

    private var positionLabel: String {
        if let index = store.detailProposalIndex {
            return "\(index + 1) of \(store.totalProposals)"
        }
        return "Proposal"
    }

    @ViewBuilder
    private func proposalNavigationBar() -> some View {
        let index = store.detailProposalIndex
        let hasPrev = (index ?? 0) > 0
        let hasNext = (index ?? 0) < store.totalProposals - 1

        VStack(spacing: 0) {
            Divider()

            HStack {
                Button {
                    store.send(.previousProposalDetail)
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "chevron.left")
                        Text("Prev")
                    }
                    .font(.system(size: 14, weight: .medium))
                    .frame(minWidth: 44, minHeight: 44)
                }
                .disabled(!hasPrev)
                .opacity(hasPrev ? 1 : 0.3)
                .accessibilityLabel("Previous proposal")

                Spacer()

                Button {
                    store.send(.nextProposalDetail)
                } label: {
                    HStack(spacing: 4) {
                        Text("Next")
                        Image(systemName: "chevron.right")
                    }
                    .font(.system(size: 14, weight: .medium))
                    .frame(minWidth: 44, minHeight: 44)
                }
                .disabled(!hasNext)
                .opacity(hasNext ? 1 : 0.3)
                .accessibilityLabel("Next proposal")
            }
            .padding(.horizontal, 24)
            .padding(.vertical, 12)
        }
    }

    // MARK: - Vote Section

    @ViewBuilder
    private func voteSection() -> some View {
        let confirmedVote = store.votes[proposal.id]
        let pendingChoice: VoteChoice? = {
            guard let pending = store.pendingVote,
                  pending.proposalId == proposal.id else { return nil }
            return pending.choice
        }()
        let votingEnabled = store.canConfirmVote

        VStack(spacing: 12) {
            if let confirmed = confirmedVote {
                if store.isSubmittingVote {
                    submittingBanner(choice: confirmed)
                } else {
                    confirmedBanner(choice: confirmed)
                }
            } else if let error = store.voteSubmissionError {
                voteErrorBanner(error: error, proposalId: proposal.id)
            } else {
                if !store.isDelegationReady {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Preparing voting credentials...")
                            .zFont(.regular, size: 13, style: Design.Text.secondary)
                    }
                    .padding(.bottom, 4)
                }

                voteButton(
                    title: "Support",
                    icon: "hand.thumbsup.fill",
                    color: .green,
                    isSelected: pendingChoice == .support,
                    enabled: votingEnabled
                ) {
                    store.send(.castVote(proposalId: proposal.id, choice: .support))
                }

                voteButton(
                    title: "Oppose",
                    icon: "hand.thumbsdown.fill",
                    color: .red,
                    isSelected: pendingChoice == .oppose,
                    enabled: votingEnabled
                ) {
                    store.send(.castVote(proposalId: proposal.id, choice: .oppose))
                }
            }
        }
    }

    @ViewBuilder
    private func confirmedBanner(choice: VoteChoice) -> some View {
        HStack(spacing: 10) {
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 20))
                .foregroundStyle(voteColor(choice))
            VStack(alignment: .leading, spacing: 2) {
                Text("Vote recorded")
                    .zFont(.semiBold, size: 15, style: Design.Text.primary)
                Text(choice.label)
                    .zFont(.medium, size: 14, style: Design.Text.secondary)
            }
            Spacer()
            VoteChip(choice: choice)
        }
        .padding(16)
        .background(voteColor(choice).opacity(0.08))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .stroke(voteColor(choice).opacity(0.2), lineWidth: 1)
        )
    }

    @ViewBuilder
    private func submittingBanner(choice: VoteChoice) -> some View {
        let step = store.voteSubmissionStep
        let stepNum = step?.stepNumber ?? 1
        let totalSteps = Voting.State.VoteSubmissionStep.totalSteps
        let stepLabel = step?.label ?? "Submitting vote..."

        VStack(spacing: 10) {
            HStack(spacing: 10) {
                ProgressView()
                VStack(alignment: .leading, spacing: 2) {
                    Text(stepLabel)
                        .zFont(.semiBold, size: 15, style: Design.Text.primary)
                    Text("Step \(stepNum) of \(totalSteps)")
                        .zFont(.regular, size: 12, style: Design.Text.tertiary)
                }
                Spacer()
                VoteChip(choice: choice)
            }

            // Step progress bar
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    RoundedRectangle(cornerRadius: 2)
                        .fill(voteColor(choice).opacity(0.15))
                    RoundedRectangle(cornerRadius: 2)
                        .fill(voteColor(choice))
                        .frame(width: geo.size.width * Double(stepNum) / Double(totalSteps))
                        .animation(.easeInOut(duration: 0.3), value: stepNum)
                }
            }
            .frame(height: 3)
        }
        .padding(16)
        .background(voteColor(choice).opacity(0.08))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .stroke(voteColor(choice).opacity(0.2), lineWidth: 1)
        )
    }

    @ViewBuilder
    private func voteErrorBanner(error: String, proposalId: UInt32) -> some View {
        VStack(spacing: 12) {
            HStack(spacing: 10) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.system(size: 20))
                    .foregroundStyle(.orange)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Vote submission failed")
                        .zFont(.semiBold, size: 15, style: Design.Text.primary)
                    Text(error)
                        .zFont(.regular, size: 13, style: Design.Text.secondary)
                        .lineLimit(3)
                }
                Spacer()
            }
            .padding(16)
            .background(Color.orange.opacity(0.08))
            .clipShape(RoundedRectangle(cornerRadius: 14))
            .overlay(
                RoundedRectangle(cornerRadius: 14)
                    .stroke(Color.orange.opacity(0.2), lineWidth: 1)
            )
        }
    }

    // MARK: - Confirmation Overlay

    @ViewBuilder
    private func confirmationOverlay() -> some View {
        let pendingChoice: VoteChoice? = {
            guard let pending = store.pendingVote,
                  pending.proposalId == proposal.id else { return nil }
            return pending.choice
        }()

        if let choice = pendingChoice {
            let canConfirm = store.canConfirmVote

            ZStack {
                // Dimmed background — tap to dismiss
                Color.black.opacity(0.5)
                    .ignoresSafeArea()
                    .onTapGesture {
                        store.send(.cancelPendingVote)
                    }

                // Overlay card
                VStack(spacing: 0) {
                    // Icon
                    ZStack {
                        Circle()
                            .fill(voteColor(choice).opacity(0.12))
                            .frame(width: 64, height: 64)
                        Image(systemName: choice == .support ? "hand.thumbsup.fill" : "hand.thumbsdown.fill")
                            .font(.system(size: 28))
                            .foregroundStyle(voteColor(choice))
                    }
                    .padding(.top, 28)
                    .padding(.bottom, 16)

                    // Title
                    Text("Confirm your vote")
                        .zFont(.semiBold, size: 20, style: Design.Text.primary)
                        .padding(.bottom, 6)

                    // Choice
                    Text(choice.label)
                        .zFont(.semiBold, size: 16, style: Design.Text.primary)
                        .foregroundStyle(voteColor(choice))
                        .padding(.bottom, 12)

                    // Warning
                    Text("This is final. Your vote will be\npublished and cannot be changed.")
                        .zFont(.medium, size: 14, style: Design.Text.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.bottom, 24)

                    // Processing state
                    if !canConfirm {
                        HStack(spacing: 8) {
                            ProgressView()
                            Text(store.isSubmittingVote
                                 ? "Submitting previous vote..."
                                 : "Preparing voting credentials...")
                                .zFont(.regular, size: 13, style: Design.Text.secondary)
                        }
                        .padding(.bottom, 16)
                    }

                    // Buttons
                    VStack(spacing: 10) {
                        Button {
                            impactFeedback.impactOccurred()
                            store.send(.confirmVote)
                        } label: {
                            Text("Confirm \(choice.label)")
                                .fontWeight(.semibold)
                                .frame(maxWidth: .infinity)
                                .padding(.vertical, 14)
                                .foregroundStyle(.white)
                                .background(canConfirm ? voteColor(choice) : voteColor(choice).opacity(0.4))
                                .clipShape(RoundedRectangle(cornerRadius: 14))
                        }
                        .disabled(!canConfirm)

                        Button {
                            store.send(.cancelPendingVote)
                        } label: {
                            Text("Go Back")
                                .zFont(.medium, size: 15, style: Design.Text.primary)
                                .frame(maxWidth: .infinity)
                                .padding(.vertical, 12)
                        }
                    }
                    .padding(.horizontal, 24)
                    .padding(.bottom, 24)
                }
                .background(Design.Surfaces.bgPrimary.color(colorScheme))
                .clipShape(RoundedRectangle(cornerRadius: 24))
                .shadow(color: .black.opacity(0.15), radius: 20, x: 0, y: 8)
                .padding(.horizontal, 32)
            }
            .transition(.opacity)
            .animation(.easeInOut(duration: 0.2), value: store.pendingVote)
        }
    }

    // MARK: - Components

    @ViewBuilder
    private func voteButton(
        title: String,
        icon: String,
        color: Color,
        isSelected: Bool,
        enabled: Bool = true,
        action: @escaping () -> Void
    ) -> some View {
        Button {
            impactFeedback.impactOccurred()
            action()
        } label: {
            HStack {
                Image(systemName: icon)
                Text(title)
                    .fontWeight(.semibold)
                Spacer()
                if isSelected {
                    Image(systemName: "checkmark.circle.fill")
                }
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 16)
            .foregroundStyle(isSelected ? .white : color)
            .background(isSelected ? color : color.opacity(enabled ? 0.1 : 0.05))
            .clipShape(RoundedRectangle(cornerRadius: 14))
            .overlay(
                RoundedRectangle(cornerRadius: 14)
                    .stroke(color.opacity(0.3), lineWidth: isSelected ? 0 : 1)
            )
        }
        .disabled(!enabled)
        .opacity(enabled ? 1 : 0.5)
    }

    private func voteColor(_ choice: VoteChoice) -> Color {
        switch choice {
        case .support: return .green
        case .oppose: return .red
        case .skip: return .gray
        }
    }
}
