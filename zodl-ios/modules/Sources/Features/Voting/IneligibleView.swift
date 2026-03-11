import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents
import VotingModels

struct IneligibleView: View {
    @Environment(\.colorScheme)
    var colorScheme

    let store: StoreOf<Voting>

    var body: some View {
        WithPerceptionTracking {
            VStack(spacing: 0) {
                ScrollView {
                    VStack(spacing: 24) {
                        Spacer().frame(height: 40)

                        // Icon
                        ZStack {
                            Circle()
                                .fill(Color.orange.opacity(0.12))
                                .frame(width: 72, height: 72)
                            Image(systemName: "exclamationmark.triangle")
                                .font(.system(size: 32))
                                .foregroundStyle(.orange)
                        }

                        // Title
                        Text("Not Eligible for This Round")
                            .zFont(.semiBold, size: 22, style: Design.Text.primary)

                        // Explanation card
                        explanationCard()

                        // Info card
                        infoCard()

                        Spacer().frame(height: 8)
                    }
                    .padding(.horizontal, 24)
                }

                ZashiButton("Close", type: .ghost) {
                    store.send(.dismissFlow)
                }
                .padding(.horizontal, 24)
                .padding(.bottom, 24)
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

    // MARK: - Explanation Card

    @ViewBuilder
    private func explanationCard() -> some View {
        let reason = store.ineligibilityReason ?? .noNotes

        VStack(alignment: .leading, spacing: 12) {
            // Description text
            switch reason {
            case .noNotes:
                // swiftlint:disable:next line_length
                Text("Your wallet has no shielded notes from before the snapshot block. Only funds that existed at block #\(snapshotHeightFormatted) are eligible for this voting round.")
                    .zFont(.regular, size: 15, style: Design.Text.secondary)

            case .balanceTooLow:
                Text("Your shielded balance at the snapshot block was \(balanceFormatted), which is below the 0.125 ZEC minimum required to vote.")
                    .zFont(.regular, size: 15, style: Design.Text.secondary)
            }

            Divider()

            // Detail rows
            detailRow(label: "Snapshot", value: "Block #\(snapshotHeightFormatted)")

            switch reason {
            case .noNotes:
                detailRow(label: "Notes found", value: "0")
            case .balanceTooLow:
                detailRow(label: "Your balance", value: "\(balanceFormatted) ZEC")
                detailRow(label: "Minimum", value: "0.125 ZEC")
                detailRow(label: "Ballots", value: "0")
            }
        }
        .padding(16)
        .background(Design.Surfaces.bgPrimary.color(colorScheme))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .stroke(Design.Surfaces.strokeSecondary.color(colorScheme), lineWidth: 1)
        )
    }

    // MARK: - Info Card

    @ViewBuilder
    private func infoCard() -> some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "info.circle")
                .font(.system(size: 14))
                .foregroundStyle(Design.Text.tertiary.color(colorScheme))

            Text("To participate in future voting rounds, ensure you have at least 0.125 ZEC in shielded funds before the snapshot block.")
                .zFont(.regular, size: 14, style: Design.Text.secondary)
        }
        .padding(14)
        .background(Design.Text.secondary.color(colorScheme).opacity(0.06))
        .clipShape(RoundedRectangle(cornerRadius: 12))
    }

    // MARK: - Helpers

    @ViewBuilder
    private func detailRow(label: String, value: String) -> some View {
        HStack {
            Text(label)
                .zFont(.medium, size: 14, style: Design.Text.tertiary)
            Spacer()
            Text(value)
                .font(.system(size: 14, weight: .medium, design: .monospaced))
                .foregroundStyle(Design.Text.primary.color(colorScheme))
        }
    }

    private var snapshotHeightFormatted: String {
        let height = store.activeSession?.snapshotHeight ?? 0
        let formatter = NumberFormatter()
        formatter.numberStyle = .decimal
        return formatter.string(from: NSNumber(value: height)) ?? "\(height)"
    }

    private var balanceFormatted: String {
        let zec = Double(store.votingWeight) / 100_000_000.0
        return String(format: "%.2f", zec)
    }
}
