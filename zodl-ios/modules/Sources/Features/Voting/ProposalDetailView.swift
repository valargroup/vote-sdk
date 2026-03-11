import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents
import VotingModels

struct ProposalDetailView: View {
    @Environment(\.colorScheme)
    var colorScheme

    let store: StoreOf<Voting>
    let proposal: Proposal

    private let impactFeedback = UIImpactFeedbackGenerator(style: .light)
    @State private var now = Date()
    private let timer = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    private var timeRemainingText: String {
        let end = store.votingRound.votingEnd
        let remaining = end.timeIntervalSince(now)
        guard remaining > 0 else { return "Ended" }

        let days = Int(remaining) / 86400
        let hours = (Int(remaining) % 86400) / 3600
        let minutes = (Int(remaining) % 3600) / 60
        let seconds = Int(remaining) % 60

        if days > 0 {
            return "\(days)d \(hours)h \(minutes)m"
        } else if hours > 0 {
            return "\(hours)h \(minutes)m \(seconds)s"
        } else {
            return "\(minutes)m \(seconds)s"
        }
    }

    var body: some View {
        WithPerceptionTracking {
            ZStack {
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
                if store.activeSession != nil {
                    ToolbarItem(placement: .navigationBarTrailing) {
                        Text(timeRemainingText)
                            .font(.system(size: 12, weight: .semibold, design: .monospaced))
                            .foregroundStyle(store.votingRound.votingEnd > now ? .green : .secondary)
                    }
                }
            }
            .onReceive(timer) { self.now = $0 }
        }
    }

    private var positionLabel: String {
        if let index = store.detailProposalIndex {
            return "\(index + 1) of \(store.totalProposals)"
        }
        return "Proposal"
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
                        if store.witnessStatus == .inProgress {
                            Text("Preparing note witnesses...")
                                .zFont(.regular, size: 13, style: Design.Text.secondary)
                        } else if case .generating(let progress) = store.delegationProofStatus {
                            Text("Preparing voting authorization... \(Int(progress * 100))%")
                                .zFont(.regular, size: 13, style: Design.Text.secondary)
                        } else {
                            Text("Preparing voting credentials...")
                                .zFont(.regular, size: 13, style: Design.Text.secondary)
                        }
                    }
                    .padding(.bottom, 4)
                } else if store.isSubmittingVote {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("Submitting vote on another proposal...")
                            .zFont(.regular, size: 13, style: Design.Text.secondary)
                    }
                    .padding(.bottom, 4)
                }

                ForEach(proposal.options, id: \.index) { option in
                    let choice = VoteChoice.option(option.index)
                    voteButton(
                        title: option.label,
                        icon: voteOptionIcon(for: option.index, total: proposal.options.count),
                        color: voteOptionColor(for: option.index, total: proposal.options.count),
                        isSelected: pendingChoice == choice,
                        enabled: votingEnabled
                    ) {
                        store.send(.castVote(proposalId: proposal.id, choice: choice))
                    }
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
                Text(optionLabel(for: choice))
                    .zFont(.medium, size: 14, style: Design.Text.secondary)
            }
            Spacer()
            VoteChip(
                choice: choice,
                label: optionLabel(for: choice),
                color: voteColor(choice)
            )
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
        let stepLabel = store.voteSubmissionStepLabel ?? "Submitting vote..."

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
                VoteChip(
                    choice: choice,
                    label: optionLabel(for: choice),
                    color: voteColor(choice)
                )
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

            Button {
                store.send(.dismissVoteError)
            } label: {
                Text("Dismiss")
                    .zFont(.medium, size: 13, style: Design.Text.primary)
            }
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

// MARK: - Confirmation Overlay & Components

extension ProposalDetailView {
    @ViewBuilder
    func confirmationOverlay() -> some View {
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
                        Image(systemName: voteOptionIcon(for: choice.index, total: proposal.options.count))
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
                    Text(optionLabel(for: choice))
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
                            Text("Confirm \(optionLabel(for: choice))")
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

    @ViewBuilder
    func voteButton(
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

    func voteColor(_ choice: VoteChoice) -> Color {
        voteOptionColor(for: choice.index, total: proposal.options.count)
    }

    func optionLabel(for choice: VoteChoice) -> String {
        proposal.options.first { $0.index == choice.index }?.label ?? "Option \(choice.index)"
    }
}
