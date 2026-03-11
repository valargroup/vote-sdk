import SwiftUI
import Generated
import UIComponents
import VotingModels

// MARK: - Prototype Banner

struct PrototypeBanner: View {
    var body: some View {
        HStack(spacing: 8) {
            Image(systemName: "wrench.and.screwdriver")
                .font(.caption)
            Text("Prototype \u{2014} some features are mocked")
                .font(.caption)
        }
        .foregroundStyle(.white)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.purple.opacity(0.8))
        .clipShape(RoundedRectangle(cornerRadius: 8))
    }
}

// MARK: - Vote Option Palette

/// Color for a vote option index. For 2-option proposals this preserves the classic
/// green (Support) / red (Oppose) look; for 3+ options it cycles through a palette.
func voteOptionColor(for index: UInt32, total: Int) -> Color {
    if total == 2 { return index == 0 ? .green : .red }
    let palette: [Color] = [.green, .red, .blue, .purple, .orange, .teal, .pink, .indigo]
    return palette[Int(index) % palette.count]
}

/// SF Symbol for a vote option index. For 2-option proposals this preserves the classic
/// thumbs-up / thumbs-down icons; for 3+ options it uses numbered circles.
func voteOptionIcon(for index: UInt32, total: Int) -> String {
    if total == 2 { return index == 0 ? "hand.thumbsup.fill" : "hand.thumbsdown.fill" }
    return "\(index + 1).circle.fill"
}

// MARK: - Vote Chip

struct VoteChip: View {
    let choice: VoteChoice?
    var label: String?
    var color: Color?

    var body: some View {
        Text(resolvedLabel)
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(choice != nil ? .white : .secondary)
            .padding(.horizontal, 10)
            .padding(.vertical, 4)
            .background(resolvedBackground)
            .clipShape(Capsule())
            .overlay(
                Capsule()
                    .stroke(borderColor, lineWidth: choice == nil ? 1 : 0)
            )
    }

    private var resolvedLabel: String {
        if let label { return label }
        guard choice != nil else { return "Not voted" }
        return "Voted"
    }

    private var resolvedBackground: Color {
        if let color { return color }
        guard choice != nil else { return .clear }
        return .gray
    }

    private var borderColor: Color {
        choice == nil ? Color.secondary.opacity(0.3) : .clear
    }
}

// MARK: - ZIP Badge

struct ZIPBadge: View {
    let zipNumber: String

    var body: some View {
        Text(zipNumber)
            .font(.system(size: 11, weight: .medium, design: .monospaced))
            .foregroundStyle(.secondary)
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(Color.secondary.opacity(0.12))
            .clipShape(Capsule())
    }
}

// MARK: - ZKP Status Banner

struct ZKPStatusBanner: View {
    let proofStatus: ProofStatus
    var isPreparingWitnesses: Bool = false

    var body: some View {
        HStack(spacing: 8) {
            switch proofStatus {
            case .notStarted:
                EmptyView()
            case .generating(let progress):
                ProgressView()
                    .scaleEffect(0.8)
                if isPreparingWitnesses {
                    Text("Preparing note witnesses...")
                        .font(.caption)
                } else {
                    Text("Preparing voting authorization... \(Int(progress * 100))%")
                        .font(.caption)
                }
            case .complete:
                Image(systemName: "checkmark.circle.fill")
                    .foregroundStyle(.green)
                    .font(.caption)
                Text("Ready to vote")
                    .font(.caption)
            case .failed(let error):
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                    .font(.caption)
                Text(error)
                    .font(.caption)
            }
        }
        .foregroundStyle(.secondary)
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.secondary.opacity(0.06))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }
}

// MARK: - Vote Commitment Stub Card

struct VoteCommitmentStubCard: View {
    let bundle: VoteCommitmentBundle
    let txHash: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Prototype VC Stub")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)

            Text("commitment: \(bundle.voteCommitment.shortHex)")
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)

            Text("van nullifier: \(bundle.vanNullifier.shortHex)")
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)

            if let txHash, !txHash.isEmpty {
                Text("tx: \(txHash)")
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.secondary.opacity(0.06))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }
}

private extension Data {
    var shortHex: String {
        let hex = map { String(format: "%02x", $0) }.joined()
        if hex.count <= 16 {
            return hex
        }
        let prefix = hex.prefix(8)
        let suffix = hex.suffix(8)
        return "\(prefix)...\(suffix)"
    }
}
