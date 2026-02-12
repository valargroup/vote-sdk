import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents
import VotingModels

struct ProposalDetailView: View {
    let store: StoreOf<Voting>
    let proposal: Proposal

    private let impactFeedback = UIImpactFeedbackGenerator(style: .light)

    var body: some View {
        WithPerceptionTracking {
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
                    store.send(.backToList)
                } label: {
                    Image(systemName: "list.bullet")
                        .font(.system(size: 16))
                        .frame(minWidth: 44, minHeight: 44)
                }
                .accessibilityLabel("Back to proposal list")

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

        VStack(spacing: 12) {
            if let confirmed = confirmedVote {
                confirmedBanner(choice: confirmed)
            } else {
                voteButton(
                    title: "Support",
                    icon: "hand.thumbsup.fill",
                    color: .green,
                    isSelected: pendingChoice == .support
                ) {
                    store.send(.castVote(proposalId: proposal.id, choice: .support))
                }

                voteButton(
                    title: "Oppose",
                    icon: "hand.thumbsdown.fill",
                    color: .red,
                    isSelected: pendingChoice == .oppose
                ) {
                    store.send(.castVote(proposalId: proposal.id, choice: .oppose))
                }

                if let pending = pendingChoice {
                    confirmationArea(choice: pending)
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
    private func confirmationArea(choice: VoteChoice) -> some View {
        VStack(spacing: 10) {
            Text("This vote cannot be changed once confirmed.")
                .zFont(.regular, size: 13, style: Design.Text.secondary)
                .multilineTextAlignment(.center)

            Button {
                impactFeedback.impactOccurred()
                store.send(.confirmVote)
            } label: {
                Text("Confirm \(choice.label)")
                    .fontWeight(.semibold)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 14)
                    .foregroundStyle(.white)
                    .background(voteColor(choice))
                    .clipShape(RoundedRectangle(cornerRadius: 14))
            }
        }
        .padding(.top, 4)
    }

    // MARK: - Components

    @ViewBuilder
    private func voteButton(
        title: String,
        icon: String,
        color: Color,
        isSelected: Bool,
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
            .background(isSelected ? color : color.opacity(0.1))
            .clipShape(RoundedRectangle(cornerRadius: 14))
            .overlay(
                RoundedRectangle(cornerRadius: 14)
                    .stroke(color.opacity(0.3), lineWidth: isSelected ? 0 : 1)
            )
        }
    }

    private func voteColor(_ choice: VoteChoice) -> Color {
        switch choice {
        case .support: return .green
        case .oppose: return .red
        case .skip: return .gray
        }
    }
}
