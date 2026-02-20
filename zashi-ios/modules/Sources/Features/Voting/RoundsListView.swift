import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents
import VotingModels

struct RoundsListView: View {
    @Environment(\.colorScheme) var colorScheme

    let store: StoreOf<Voting>

    var body: some View {
        WithPerceptionTracking {
            VStack(spacing: 0) {
                // Segmented tab bar
                tabBar()

                // Round cards
                ScrollView {
                    if store.allRounds.isEmpty {
                        loadingState()
                    } else if store.visibleRounds.isEmpty {
                        emptyState()
                    } else {
                        LazyVStack(spacing: 12) {
                            ForEach(store.visibleRounds) { item in
                                roundCard(item)
                            }
                        }
                        .padding(.horizontal, 24)
                        .padding(.vertical, 16)
                    }
                }
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

    // MARK: - Tab Bar

    @ViewBuilder
    private func tabBar() -> some View {
        HStack(spacing: 0) {
            tabButton(.active, label: "Active", count: store.activeRounds.count)
            tabButton(.completed, label: "Completed", count: store.completedRounds.count)
        }
        .padding(.horizontal, 24)
        .padding(.top, 8)
        .padding(.bottom, 4)
    }

    @ViewBuilder
    private func tabButton(_ tab: Voting.State.RoundTab, label: String, count: Int) -> some View {
        let isSelected = store.selectedTab == tab
        Button {
            store.send(.selectTab(tab))
        } label: {
            HStack(spacing: 6) {
                Text(label)
                    .zFont(isSelected ? .semiBold : .medium, size: 14,
                           style: isSelected ? Design.Text.primary : Design.Text.tertiary)

                Text("\(count)")
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(isSelected ? .white : Design.Text.tertiary.color(colorScheme))
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(isSelected ? Color.accentColor : Color.secondary.opacity(0.2))
                    .clipShape(Capsule())
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 10)
        }
        .overlay(alignment: .bottom) {
            if isSelected {
                Rectangle()
                    .fill(Color.accentColor)
                    .frame(height: 2)
            }
        }
    }

    // MARK: - Round Card

    @ViewBuilder
    private func roundCard(_ item: Voting.State.RoundListItem) -> some View {
        let session = item.session

        VStack(alignment: .leading, spacing: 10) {
            // Header: title + status badge
            HStack {
                Text(item.title)
                    .zFont(.semiBold, size: 16, style: Design.Text.primary)

                Spacer()

                statusBadge(session.status)

                Image(systemName: "chevron.right")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(Design.Text.tertiary.color(colorScheme))
            }

            // Description (2 lines max)
            if !session.description.isEmpty {
                Text(session.description)
                    .zFont(.regular, size: 13, style: Design.Text.secondary)
                    .lineLimit(2)
            }

            // Footer: proposal count + end date
            HStack {
                HStack(spacing: 4) {
                    Image(systemName: "doc.text")
                        .font(.system(size: 11))
                        .foregroundStyle(Design.Text.tertiary.color(colorScheme))
                    Text("\(session.proposals.count) proposals")
                        .zFont(.medium, size: 12, style: Design.Text.tertiary)
                }

                Spacer()

                let endLabel = session.status == .finalized ? "Ended" : "Ends"
                Text("\(endLabel) \(session.voteEndTime.formatted(date: .abbreviated, time: .omitted))")
                    .zFont(.medium, size: 12, style: Design.Text.tertiary)
            }
        }
        .padding(16)
        .background(Design.Surfaces.bgPrimary.color(colorScheme))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .stroke(Design.Surfaces.strokeSecondary.color(colorScheme), lineWidth: 1)
        )
        .shadow(color: .black.opacity(0.04), radius: 2, x: 0, y: 1)
        .contentShape(Rectangle())
        .onTapGesture {
            store.send(.roundTapped(item.id))
        }
    }

    // MARK: - Status Badge

    @ViewBuilder
    private func statusBadge(_ status: SessionStatus) -> some View {
        let (label, color) = statusInfo(status)

        Text(label)
            .font(.system(size: 11, weight: .semibold))
            .foregroundStyle(color)
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(color.opacity(0.12))
            .clipShape(Capsule())
    }

    private func statusInfo(_ status: SessionStatus) -> (String, Color) {
        switch status {
        case .active: return ("Active", .green)
        case .tallying: return ("Tallying", .orange)
        case .finalized: return ("Finalized", .blue)
        case .unspecified: return ("Unknown", .secondary)
        }
    }

    // MARK: - Empty & Loading States

    @ViewBuilder
    private func loadingState() -> some View {
        VStack(spacing: 12) {
            Spacer().frame(height: 60)
            ProgressView()
            Text("Loading rounds...")
                .zFont(.regular, size: 14, style: Design.Text.secondary)
            Spacer()
        }
        .frame(maxWidth: .infinity)
    }

    @ViewBuilder
    private func emptyState() -> some View {
        let tabName = store.selectedTab == .active ? "active" : "completed"

        VStack(spacing: 12) {
            Spacer().frame(height: 60)

            Image(systemName: store.selectedTab == .active ? "rectangle.slash" : "checkmark.rectangle")
                .font(.system(size: 28))
                .foregroundStyle(Design.Text.tertiary.color(colorScheme))

            Text("No \(tabName) rounds")
                .zFont(.semiBold, size: 18, style: Design.Text.primary)

            Text(store.selectedTab == .active
                 ? "There are no voting rounds in progress right now."
                 : "No rounds have been finalized yet.")
                .zFont(.regular, size: 13, style: Design.Text.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)

            Spacer()
        }
        .frame(maxWidth: .infinity)
    }
}
