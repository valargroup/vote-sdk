import SwiftUI
import Combine
import ComposableArchitecture
import Generated
import UIComponents
import VotingModels

struct ProposalListView: View {
    @Environment(\.colorScheme)
    var colorScheme
    @State private var showSnapshotHeight = false
    @State private var now = Date()

    let store: StoreOf<Voting>

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
            VStack(spacing: 0) {
                proposalScrollView()
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

    // MARK: - Scroll View

    @ViewBuilder
    private func proposalScrollView() -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(spacing: 12) {
                    if store.activeSession == nil {
                        noActiveRoundCard()
                    } else {
                        roundInfoCard()
                        zkpBanner()
                        progressHeader()

                        ForEach(store.votingRound.proposals) { proposal in
                            proposalCard(proposal)
                                .id(proposal.id)
                        }
                    }
                }
                .padding(.horizontal, 24)
                .padding(.bottom, 24)
            }
            .onAppear {
                if let id = store.activeProposalId {
                    proxy.scrollTo(id, anchor: .center)
                }
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
        VStack(spacing: 10) {
            Text(store.votingRound.title)
                .zFont(.semiBold, size: 15, style: Design.Text.primary)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)

            HStack(spacing: 0) {
                detailPill(
                    label: "Snapshot",
                    value: store.votingRound.snapshotDate.formatted(date: .abbreviated, time: .omitted)
                )
                .frame(maxWidth: .infinity, alignment: .leading)
                .onTapGesture {
                    showSnapshotHeight = true
                }
                .popover(isPresented: $showSnapshotHeight) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Block #\(store.votingRound.snapshotHeight.formatted())")
                        Text(store.votingRound.snapshotDate.formatted(date: .abbreviated, time: .standard))
                    }
                    .font(.system(size: 13, weight: .medium, design: .monospaced))
                    .padding(12)
                    .compactPopover()
                }

                detailPill(
                    label: "Ends",
                    value: store.votingRound.votingEnd.formatted(date: .abbreviated, time: .omitted)
                )
                .frame(maxWidth: .infinity)

                detailPill(
                    label: "Eligible",
                    value: "\(store.votingWeightZECString) ZEC"
                )
                .frame(maxWidth: .infinity, alignment: .trailing)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.horizontal, 16)
        .padding(.vertical, 16)
        .background(Design.Surfaces.bgPrimary.color(colorScheme))
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .overlay(
            RoundedRectangle(cornerRadius: 12)
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

    // MARK: - No Active Round

    @ViewBuilder
    private func noActiveRoundCard() -> some View {
        VStack(spacing: 12) {
            Image(systemName: "rectangle.slash")
                .font(.system(size: 28))
                .foregroundStyle(Design.Text.tertiary.color(colorScheme))

            Text("No Active Voting Round")
                .zFont(.semiBold, size: 18, style: Design.Text.primary)

            Text("There are no voting rounds in progress. Rounds are created by governance administrators — check back later.")
                .zFont(.regular, size: 13, style: Design.Text.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(20)
        .background(Design.Surfaces.bgPrimary.color(colorScheme))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .stroke(Design.Surfaces.strokeSecondary.color(colorScheme), lineWidth: 1)
        )
        .padding(.top, 8)
    }

    // MARK: - Status

    @ViewBuilder
    private func zkpBanner() -> some View {
        if store.delegationProofStatus != .notStarted && store.delegationProofStatus != .complete {
            ZKPStatusBanner(
                proofStatus: store.delegationProofStatus,
                isPreparingWitnesses: store.witnessStatus == .inProgress
            )
        }
    }

    @ViewBuilder
    private func progressHeader() -> some View {
        if store.activeSession != nil {
            HStack {
                Text("\(store.votedCount) of \(store.totalProposals) voted")
                    .zFont(.medium, size: 14, style: Design.Text.secondary)

                Spacer()

                if store.isDelegationReady {
                    HStack(spacing: 4) {
                        Image(systemName: "checkmark.circle.fill")
                            .foregroundStyle(.green)
                            .font(.system(size: 12))
                        Text("Ready to vote")
                            .font(.system(size: 12, weight: .medium))
                            .foregroundStyle(.green)
                    }
                }
            }
        }
    }
}

// MARK: - Card

extension ProposalListView {
    @ViewBuilder
    func proposalCard(_ proposal: Proposal) -> some View {
        let vote = store.votes[proposal.id]

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
                    if store.submittingProposalId == proposal.id {
                        HStack(spacing: 4) {
                            ProgressView()
                                .scaleEffect(0.7)
                            VoteChip(
                                choice: vote,
                                label: vote.flatMap { voteChoice in proposal.options.first { $0.index == voteChoice.index }?.label },
                                color: vote.map { voteOptionColor(for: $0.index, total: proposal.options.count) }
                            )
                        }
                    } else {
                        VoteChip(
                            choice: vote,
                            label: vote.flatMap { voteChoice in proposal.options.first { $0.index == voteChoice.index }?.label },
                            color: vote.map { voteOptionColor(for: $0.index, total: proposal.options.count) }
                        )
                    }
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
        .background(Design.Surfaces.bgPrimary.color(colorScheme))
        .clipShape(RoundedRectangle(cornerRadius: 16))
        .overlay(
            RoundedRectangle(cornerRadius: 16)
                .stroke(
                    vote != nil
                        ? voteColor(vote, proposal: proposal).opacity(0.3)
                        : Design.Surfaces.strokeSecondary.color(colorScheme),
                    lineWidth: 1
                )
        )
        .shadow(color: .black.opacity(0.04), radius: 2, x: 0, y: 1)
        .contentShape(Rectangle())
        .onTapGesture {
            store.send(.proposalTapped(proposal.id))
        }
    }
}

// MARK: - Helpers

extension ProposalListView {
    func voteColor(_ vote: VoteChoice?, proposal: Proposal) -> Color {
        guard let vote else { return .clear }
        return voteOptionColor(for: vote.index, total: proposal.options.count)
    }
}

private extension View {
    @ViewBuilder
    func compactPopover() -> some View {
        if #available(iOS 16.4, *) {
            self.presentationCompactAdaptation(.popover)
        } else {
            self
        }
    }
}
