import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents

struct ProposalListView: View {
    @Environment(\.colorScheme) var colorScheme

    let store: StoreOf<Voting>

    private let selectionFeedback = UISelectionFeedbackGenerator()
    private let impactFeedback = UIImpactFeedbackGenerator(style: .light)

    var body: some View {
        WithPerceptionTracking {
            VStack(spacing: 0) {
                proposalScrollView()
                bottomBar()
            }
            .navigationTitle("Governance")
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

    // MARK: - Scroll View

    @ViewBuilder
    private func proposalScrollView() -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(spacing: 12) {
                    roundInfoCard()
                    zkpBanner()
                    progressHeader()

                    ForEach(store.votingRound.proposals) { proposal in
                        proposalCard(proposal)
                            .id(proposal.id)
                    }
                }
                .padding(.horizontal, 24)
                .padding(.bottom, 24)
            }
            .onChange(of: store.activeProposalId) { newId in
                if let newId {
                    withAnimation(.easeInOut(duration: 0.2)) {
                        proxy.scrollTo(newId, anchor: .center)
                    }
                }
            }
        }
    }

    // MARK: - Round Info Header

    @ViewBuilder
    private func roundInfoCard() -> some View {
        VStack(spacing: 12) {
            // Title row
            HStack(alignment: .top) {
                Text(store.votingRound.title)
                    .zFont(.semiBold, size: 18, style: Design.Text.primary)
                Spacer()
                Text("\(store.votingRound.daysRemaining)d left")
                    .zFont(.medium, size: 13, style: Design.Text.secondary)
            }

            // Detail grid
            HStack(spacing: 0) {
                detailPill(
                    label: "Snapshot",
                    value: "#\(store.votingRound.snapshotHeight.formatted())"
                )
                Spacer()
                detailPill(
                    label: "Ends",
                    value: store.votingRound.votingEnd.formatted(date: .abbreviated, time: .omitted)
                )
                Spacer()
                detailPill(
                    label: "Weight",
                    value: "\(store.votingWeightZECString) ZEC"
                )
            }
        }
        .padding(16)
        .background(Design.Surfaces.bgPrimary.color(colorScheme))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .stroke(Design.Surfaces.strokeSecondary.color(colorScheme), lineWidth: 1)
        )
        .padding(.top, 8)
    }

    @ViewBuilder
    private func detailPill(label: String, value: String) -> some View {
        VStack(spacing: 2) {
            Text(label)
                .font(.system(size: 10, weight: .medium))
                .foregroundStyle(.secondary)
            Text(value)
                .font(.system(size: 12, weight: .semibold, design: .monospaced))
                .foregroundStyle(Design.Text.primary.color(colorScheme))
        }
    }

    // MARK: - Status

    @ViewBuilder
    private func zkpBanner() -> some View {
        if store.delegationProofStatus != .notStarted && store.delegationProofStatus != .complete {
            ZKPStatusBanner(proofStatus: store.delegationProofStatus)
        }
    }

    @ViewBuilder
    private func progressHeader() -> some View {
        HStack {
            Text("\(store.votedCount) of \(store.totalProposals) voted")
                .zFont(.medium, size: 14, style: Design.Text.secondary)

            Spacer()

            if store.isDelegationReady {
                HStack(spacing: 4) {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                        .font(.system(size: 12))
                    Text("Delegation ready")
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(.green)
                }
            }
        }
    }

    // MARK: - Bottom Bar

    @ViewBuilder
    private func bottomBar() -> some View {
        let activeProposal = store.activeProposalId
            .flatMap { id in store.votingRound.proposals.first { $0.id == id } }
        let activeIndex = store.activeProposalId
            .flatMap { id in store.votingRound.proposals.firstIndex { $0.id == id } }

        VStack(spacing: 0) {
            Divider()

            if store.allVoted {
                reviewBar()
            } else if let proposal = activeProposal {
                voteBar(proposal: proposal, vote: store.votes[proposal.id], activeIndex: activeIndex)
            }
        }
        .background(Design.Surfaces.bgPrimary.color(colorScheme))
    }

    @ViewBuilder
    private func reviewBar() -> some View {
        VStack(spacing: 8) {
            Text("All proposals voted!")
                .zFont(.medium, size: 13, style: Design.Text.secondary)

            ZashiButton(
                "Review & Submit",
                type: store.canSubmitVotes ? .primary : .quaternary
            ) {
                store.send(.reviewVotesTapped)
            }
            .disabled(!store.canSubmitVotes)
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 12)
    }

    @ViewBuilder
    private func voteBar(proposal: Proposal, vote: VoteChoice?, activeIndex: Int?) -> some View {
        let hasPrev = (activeIndex ?? 0) > 0
        let hasNext = (activeIndex ?? 0) < store.totalProposals - 1

        VStack(spacing: 10) {
            voteBarTitle(proposal: proposal, vote: vote, hasPrev: hasPrev, hasNext: hasNext)
            voteBarButtons(proposal: proposal, vote: vote)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 12)
    }

    @ViewBuilder
    private func voteBarTitle(proposal: Proposal, vote: VoteChoice?, hasPrev: Bool, hasNext: Bool) -> some View {
        HStack(spacing: 0) {
            Button {
                selectionFeedback.selectionChanged()
                store.send(.bottomBarPrevious)
            } label: {
                Image(systemName: "chevron.left")
                    .font(.system(size: 14, weight: .semibold))
                    .frame(minWidth: 44, minHeight: 44)
            }
            .disabled(!hasPrev)
            .opacity(hasPrev ? 1 : 0.3)
            .accessibilityLabel("Previous proposal")

            VStack(alignment: .leading, spacing: 2) {
                if let zip = proposal.zipNumber {
                    Text(zip)
                        .font(.system(size: 11, weight: .medium, design: .monospaced))
                        .foregroundStyle(.secondary)
                }
                Text(proposal.title)
                    .zFont(.medium, size: 13, style: Design.Text.primary)
                    .lineLimit(1)
            }

            Spacer()

            if let vote {
                VoteChip(choice: vote)
            }

            Button {
                selectionFeedback.selectionChanged()
                store.send(.bottomBarNext)
            } label: {
                Image(systemName: "chevron.right")
                    .font(.system(size: 14, weight: .semibold))
                    .frame(minWidth: 44, minHeight: 44)
            }
            .disabled(!hasNext)
            .opacity(hasNext ? 1 : 0.3)
            .accessibilityLabel("Next proposal")
        }
    }

    @ViewBuilder
    private func voteBarButtons(proposal: Proposal, vote: VoteChoice?) -> some View {
        HStack(spacing: 8) {
            bottomVoteButton("Support", color: .green, icon: "hand.thumbsup", isSelected: vote == .support) {
                store.send(.castVote(proposalId: proposal.id, choice: .support))
            }
            bottomVoteButton("Oppose", color: .red, icon: "hand.thumbsdown", isSelected: vote == .oppose) {
                store.send(.castVote(proposalId: proposal.id, choice: .oppose))
            }
            bottomVoteButton("Skip", color: .gray, icon: "forward", isSelected: vote == .skip) {
                store.send(.castVote(proposalId: proposal.id, choice: .skip))
            }
        }
    }

    // MARK: - Card

    @ViewBuilder
    private func proposalCard(_ proposal: Proposal) -> some View {
        let vote = store.votes[proposal.id]
        let isActive = store.activeProposalId == proposal.id

        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 4) {
                    if let zip = proposal.zipNumber {
                        ZIPBadge(zipNumber: zip)
                    }
                    Text(proposal.title)
                        .zFont(.semiBold, size: 16, style: Design.Text.primary)
                }

                Spacer(minLength: 8)

                if vote != nil {
                    VoteChip(choice: vote)
                }

                Image(systemName: "chevron.right")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(Design.Text.tertiary.color(colorScheme))
            }

            Text(proposal.description)
                .zFont(.regular, size: 13, style: Design.Text.secondary)
                .lineLimit(2)
        }
        .padding(16)
        .background(
            isActive
                ? Design.Surfaces.brandPrimary.color(colorScheme).opacity(0.04)
                : Design.Surfaces.bgPrimary.color(colorScheme)
        )
        .clipShape(RoundedRectangle(cornerRadius: 16))
        .overlay(
            RoundedRectangle(cornerRadius: 16)
                .stroke(
                    isActive
                        ? Design.Surfaces.brandPrimary.color(colorScheme)
                        : vote != nil
                            ? voteColor(vote).opacity(0.3)
                            : Design.Surfaces.strokeSecondary.color(colorScheme),
                    lineWidth: isActive ? 2 : 1
                )
        )
        .shadow(color: .black.opacity(0.04), radius: 2, x: 0, y: 1)
        .contentShape(Rectangle())
        .onTapGesture {
            store.send(.proposalTapped(proposal.id))
        }
    }

    // MARK: - Components

    @ViewBuilder
    private func bottomVoteButton(
        _ title: String,
        color: Color,
        icon: String,
        isSelected: Bool = false,
        action: @escaping () -> Void
    ) -> some View {
        Button {
            impactFeedback.impactOccurred()
            action()
        } label: {
            HStack(spacing: 4) {
                Image(systemName: icon)
                    .font(.system(size: 12))
                Text(title)
                    .font(.system(size: 14, weight: .semibold))
            }
            .foregroundStyle(isSelected ? .white : color)
            .frame(maxWidth: .infinity)
            .frame(minHeight: 44)
            .background(isSelected ? color : color.opacity(0.1))
            .clipShape(RoundedRectangle(cornerRadius: 12))
        }
    }

    private func voteColor(_ vote: VoteChoice?) -> Color {
        guard let vote else { return .clear }
        switch vote {
        case .support: return .green
        case .oppose: return .red
        case .skip: return .gray
        }
    }
}
