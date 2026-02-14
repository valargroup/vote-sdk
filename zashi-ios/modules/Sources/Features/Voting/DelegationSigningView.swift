import SwiftUI
import ComposableArchitecture
import Generated
import UIComponents
import VotingModels

struct DelegationSigningView: View {
    @Environment(\.colorScheme) var colorScheme

    let store: StoreOf<Voting>

    var body: some View {
        WithPerceptionTracking {
            VStack(spacing: 0) {
                ScrollView {
                    transactionSummary()
                    noteVerificationSection()
                }
                .padding(.vertical, 1)

                Spacer()

                actionButton()
                    .padding(.horizontal, 24)
                    .padding(.bottom, 24)
            }
        }
        .screenTitle("Authorize Voting")
        .zashiBack {
            store.send(.delegationRejected)
        }
        .navigationBarBackButtonHidden()
    }

    // MARK: - Transaction Summary (matches SendConfirmation layout)

    @ViewBuilder
    private func transactionSummary() -> some View {
        VStack(spacing: 0) {
            // Voting weight summary (centered)
            VStack(spacing: 0) {
                Text("Eligible Funds")
                    .zFont(size: 14, style: Design.Text.primary)
                    .padding(.bottom, 2)

                Text("\(store.votingWeightZECString) ZEC")
                    .zFont(.semiBold, size: 28, style: Design.Text.primary)

                Text("Authorize a hotkey to vote on your behalf")
                    .zFont(.medium, size: 14, style: Design.Text.tertiary)
                    .multilineTextAlignment(.center)
                    .padding(.top, 6)
            }
            .padding(.top, 40)
            .padding(.bottom, 20)

            // Hotkey address
            detailSection(label: "Voting hotkey") {
                Text(store.hotkeyAddress ?? "")
                    .zFont(addressFont: true, size: 12, style: Design.Text.primary)
                    .onTapGesture {
                        store.send(.copyHotkeyAddress)
                    }
            }

            // Round
            detailRow(label: "Round", value: store.votingRound.title)

            // Memo
            memoSection()

            // Keystone device (if applicable)
            if store.isKeystoneUser {
                keystoneDevice()
                    .padding(.horizontal, 24)
                    .padding(.top, 20)
            }
        }
    }

    @ViewBuilder
    private func detailSection(label: String, @ViewBuilder content: () -> some View) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 6) {
                Text(label)
                    .zFont(.medium, size: 14, style: Design.Text.tertiary)
                content()
            }
            Spacer()
        }
        .padding(.horizontal, 24)
        .padding(.bottom, 20)
    }

    @ViewBuilder
    private func detailRow(label: String, value: String) -> some View {
        HStack {
            Text(label)
                .zFont(.medium, size: 14, style: Design.Text.tertiary)
            Spacer()
            Text(value)
                .zFont(.semiBold, size: 14, style: Design.Text.primary)
        }
        .padding(.horizontal, 24)
        .padding(.bottom, 20)
    }

    @ViewBuilder
    private func memoSection() -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Memo")
                .zFont(.medium, size: 14, style: Design.Text.tertiary)

            HStack {
                Text("Delegating \(store.votingWeightZECString) ZEC of eligible notes to vote in \(store.votingRound.title)")
                    .zFont(.medium, size: 14, style: Design.Inputs.Filled.text)
                Spacer(minLength: 0)
            }
            .frame(maxWidth: .infinity)
            .padding()
            .background {
                RoundedRectangle(cornerRadius: Design.Radius._lg)
                    .fill(Design.Inputs.Filled.bg.color(colorScheme))
            }
        }
        .padding(.horizontal, 24)
        .padding(.bottom, 20)
    }

    // MARK: - Note Verification

    @ViewBuilder
    private func noteVerificationSection() -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Note Verification")
                .zFont(.semiBold, size: 14, style: Design.Text.primary)

            switch store.witnessStatus {
            case .notStarted:
                EmptyView()

            case .inProgress:
                HStack(spacing: 8) {
                    ProgressView()
                    Text("Verifying note witnesses...")
                        .zFont(.medium, size: 14, style: Design.Text.tertiary)
                }

            case .completed:
                ForEach(store.noteWitnessResults) { result in
                    noteResultRow(result)
                }

                let passCount = store.noteWitnessResults.filter(\.verified).count
                let total = store.noteWitnessResults.count
                Text("\(passCount)/\(total) notes verified")
                    .zFont(.medium, size: 13, style: passCount == total
                        ? Design.Text.primary
                        : Design.Text.tertiary
                    )

                if let timing = store.witnessTiming {
                    timingBreakdown(timing)
                }

                Button {
                    store.send(.rerunWitnessVerification)
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "arrow.clockwise")
                            .font(.system(size: 11))
                        Text("Re-verify (invalidate cache)")
                            .zFont(.medium, size: 12, style: Design.Text.tertiary)
                    }
                }
                .padding(.top, 4)

            case .failed(let error):
                HStack(spacing: 6) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.red)
                    Text("Verification failed: \(error)")
                        .zFont(.medium, size: 13, style: Design.Text.tertiary)
                        .lineLimit(3)
                }
            }
        }
        .padding(.horizontal, 24)
        .padding(.bottom, 20)
    }

    @ViewBuilder
    private func noteResultRow(_ result: Voting.State.NoteWitnessResult) -> some View {
        HStack(spacing: 8) {
            Image(systemName: result.verified ? "checkmark.circle.fill" : "xmark.circle.fill")
                .foregroundStyle(result.verified ? .green : .red)
                .font(.system(size: 14))

            let zec = Double(result.value) / 100_000_000.0
            Text(String(format: "%.2f ZEC", zec))
                .zFont(.medium, size: 14, style: Design.Text.primary)

            Text("pos \(result.position)")
                .zFont(size: 12, style: Design.Text.tertiary)

            Spacer()

            Text(result.verified ? "PASS" : "FAIL")
                .zFont(.semiBold, size: 12, style: Design.Text.primary)
                .foregroundStyle(result.verified ? .green : .red)
        }
    }

    @ViewBuilder
    private func timingBreakdown(_ timing: Voting.State.WitnessTiming) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            timingRow("Tree state fetch", ms: timing.treeStateFetchMs)
            timingRow("Witness generation", ms: timing.witnessGenerationMs)
            timingRow("Verification", ms: timing.verificationMs)
            Divider()
            timingRow("Total", ms: timing.totalMs)
        }
        .padding(10)
        .background {
            RoundedRectangle(cornerRadius: 8)
                .fill(Design.Inputs.Filled.bg.color(colorScheme))
        }
    }

    @ViewBuilder
    private func timingRow(_ label: String, ms: UInt64) -> some View {
        HStack {
            Text(label)
                .zFont(size: 12, style: Design.Text.tertiary)
            Spacer()
            Text(ms >= 1000 ? String(format: "%.1fs", Double(ms) / 1000.0) : "\(ms)ms")
                .zFont(.medium, size: 12, style: Design.Text.primary)
        }
    }

    // MARK: - Action Button

    @ViewBuilder
    private func actionButton() -> some View {
        let witnessReady = store.witnessStatus == .completed
            && store.noteWitnessResults.allSatisfy(\.verified)

        if store.isKeystoneUser {
            ZashiButton("Confirm with Keystone") {
                store.send(.delegationApproved)
            }
            .disabled(!witnessReady)
            .opacity(witnessReady ? 1.0 : 0.5)
        } else {
            ZashiButton("Authorize Voting") {
                store.send(.delegationApproved)
            }
            .disabled(!witnessReady)
            .opacity(witnessReady ? 1.0 : 0.5)
        }
    }

    // MARK: - Keystone Device Simulation

    private let keystoneYellow = Color(red: 0.95, green: 0.78, blue: 0.15)
    private let keystoneCyan = Color(red: 0.45, green: 0.85, blue: 0.9)

    @ViewBuilder
    private func keystoneDevice() -> some View {
        VStack(spacing: 12) {
            VStack(spacing: 0) {
                // Status bar
                HStack {
                    HStack(spacing: 4) {
                        Image(systemName: "antenna.radiowaves.left.and.right")
                            .font(.system(size: 10))
                        Text("ADAM")
                            .font(.system(size: 12, weight: .semibold))
                    }
                    Spacer()
                    Image(systemName: "battery.75")
                        .font(.system(size: 14))
                }
                .foregroundStyle(.white.opacity(0.6))
                .padding(.horizontal, 16)
                .padding(.top, 10)
                .padding(.bottom, 6)

                // Title
                VStack(spacing: 6) {
                    ZStack {
                        Circle()
                            .fill(keystoneYellow)
                            .frame(width: 28, height: 28)
                        Text("Z")
                            .font(.system(size: 16, weight: .black))
                            .foregroundStyle(.black)
                    }
                    Text("Confirm Transaction")
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(.white)
                }
                .padding(.bottom, 14)

                // Transaction outputs
                VStack(alignment: .leading, spacing: 14) {
                    VStack(alignment: .leading, spacing: 3) {
                        Text("To")
                            .font(.system(size: 11))
                            .foregroundStyle(.white.opacity(0.5))
                        HStack(spacing: 6) {
                            Text("#1")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundStyle(.white)
                            Text("0.00 ZEC")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundStyle(keystoneYellow)
                            Text("Change")
                                .font(.system(size: 9, weight: .medium))
                                .foregroundStyle(.white.opacity(0.7))
                                .padding(.horizontal, 5)
                                .padding(.vertical, 2)
                                .overlay(
                                    RoundedRectangle(cornerRadius: 3)
                                        .stroke(.white.opacity(0.3), lineWidth: 1)
                                )
                        }
                        Text("<internal-address>")
                            .font(.system(size: 11))
                            .foregroundStyle(keystoneCyan)
                        Text("Memo: Delegating \(store.votingWeightZECString) ZEC of eligible notes")
                            .font(.system(size: 10))
                            .foregroundStyle(.white.opacity(0.6))
                    }

                    VStack(alignment: .leading, spacing: 3) {
                        HStack(spacing: 6) {
                            Text("#2")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundStyle(.white)
                            Text("0.00 ZEC")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundStyle(keystoneYellow)
                        }
                        Text(store.hotkeyAddress ?? "")
                            .font(.system(size: 11, design: .monospaced))
                            .foregroundStyle(keystoneCyan)
                            .lineLimit(3)
                    }
                }
                .padding(.horizontal, 16)

                Spacer().frame(height: 16)
            }
            .frame(maxWidth: .infinity)
            .background(Color(red: 0.08, green: 0.08, blue: 0.1))
            .clipShape(RoundedRectangle(cornerRadius: 14))
            .overlay(
                RoundedRectangle(cornerRadius: 14)
                    .stroke(Color(white: 0.3), lineWidth: 2)
            )

            mockLabel("Simulated Keystone screen")
        }
    }

    // MARK: - Helpers

    @ViewBuilder
    private func mockLabel(_ text: String) -> some View {
        HStack(spacing: 6) {
            Text("MOCK")
                .font(.system(size: 9, weight: .bold, design: .rounded))
                .foregroundStyle(.white)
                .padding(.horizontal, 5)
                .padding(.vertical, 2)
                .background(Color.orange)
                .clipShape(Capsule())
            Text(text)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }
}
