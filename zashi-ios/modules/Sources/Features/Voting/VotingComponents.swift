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

// MARK: - Vote Chip

struct VoteChip: View {
    let choice: VoteChoice?

    var body: some View {
        Text(label)
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(foregroundColor)
            .padding(.horizontal, 10)
            .padding(.vertical, 4)
            .background(backgroundColor)
            .clipShape(Capsule())
            .overlay(
                Capsule()
                    .stroke(borderColor, lineWidth: choice == nil ? 1 : 0)
            )
    }

    private var label: String {
        guard let choice else { return "Not voted" }
        return choice.label
    }

    private var foregroundColor: Color {
        guard let choice else { return .secondary }
        switch choice {
        case .support: return .white
        case .oppose: return .white
        case .skip: return .white
        }
    }

    private var backgroundColor: Color {
        guard let choice else { return .clear }
        switch choice {
        case .support: return .green
        case .oppose: return .red
        case .skip: return .gray
        }
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
                Text("Proof failed: \(error)")
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

