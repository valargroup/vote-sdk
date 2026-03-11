import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents

struct WalletSyncingView: View {
    @Environment(\.colorScheme)
    var colorScheme

    let store: StoreOf<Voting>

    var body: some View {
        WithPerceptionTracking {
            VStack(spacing: 0) {
                Spacer()

                VStack(spacing: 24) {
                    // Icon
                    ZStack {
                        Circle()
                            .fill(Color.orange.opacity(0.12))
                            .frame(width: 72, height: 72)
                        Image(systemName: "arrow.triangle.2.circlepath")
                            .font(.system(size: 32))
                            .foregroundStyle(.orange)
                    }

                    // Title
                    Text("Wallet Still Syncing")
                        .zFont(.semiBold, size: 22, style: Design.Text.primary)

                    // Description
                    Text("Your wallet needs to sync past the snapshot block before you can participate in governance voting.")
                        .zFont(.regular, size: 15, style: Design.Text.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 32)

                    // Progress card
                    syncProgressCard()
                        .padding(.horizontal, 24)

                    // Info card
                    HStack(alignment: .top, spacing: 10) {
                        Image(systemName: "info.circle")
                            .font(.system(size: 14))
                            .foregroundStyle(Design.Text.tertiary.color(colorScheme))

                        Text("Please wait for your wallet to finish syncing, then come back to vote.")
                            .zFont(.regular, size: 14, style: Design.Text.secondary)
                    }
                    .padding(14)
                    .background(Design.Text.secondary.color(colorScheme).opacity(0.06))
                    .clipShape(RoundedRectangle(cornerRadius: 12))
                    .padding(.horizontal, 24)
                }

                Spacer()

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

    // MARK: - Sync Progress Card

    @ViewBuilder
    private func syncProgressCard() -> some View {
        let snapshotHeight = store.activeSession?.snapshotHeight ?? 0
        let scannedHeight = store.walletScannedHeight

        VStack(alignment: .leading, spacing: 12) {
            detailRow(label: "Wallet synced to", value: "Block #\(formatted(scannedHeight))")
            detailRow(label: "Snapshot block", value: "Block #\(formatted(snapshotHeight))")

            if snapshotHeight > 0 {
                Divider()

                let remaining = snapshotHeight > scannedHeight ? snapshotHeight - scannedHeight : 0
                detailRow(label: "Blocks remaining", value: formatted(remaining))
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

    private func formatted(_ value: UInt64) -> String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .decimal
        return formatter.string(from: NSNumber(value: value)) ?? "\(value)"
    }
}
