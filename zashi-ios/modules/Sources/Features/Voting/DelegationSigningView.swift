import SwiftUI
import ComposableArchitecture
import Generated
import SDKSynchronizer
import UIComponents
import Vendors
import VotingModels

struct DelegationSigningView: View {
    @Environment(\.colorScheme) var colorScheme
    @Dependency(\.sdkSynchronizer) var sdkSynchronizer

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
                keystoneSigningSection()
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
        let zec = Double(store.votingWeight) / 100_000_000.0
        let memoAmount = String(format: "%.8f", zec)

        VStack(alignment: .leading, spacing: 6) {
            Text("Memo")
                .zFont(.medium, size: 14, style: Design.Text.tertiary)

            HStack {
                Text("I am authorizing this hotkey managed by my wallet to vote on \(store.votingRound.title) with \(memoAmount) ZEC.")
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
            switch store.keystoneSigningStatus {
            case .idle:
                ZashiButton("Confirm with Keystone") {
                    store.send(.delegationApproved)
                }
                .disabled(!witnessReady)
                .opacity(witnessReady ? 1.0 : 0.5)

            case .preparingRequest:
                ZashiButton("Preparing Keystone request...") { }
                    .disabled(true)
                    .opacity(0.5)

            case .awaitingSignature:
                VStack(spacing: 8) {
                    ZashiButton("Scan Signed Keystone QR") {
                        store.send(.openKeystoneSignatureScan)
                    }
                    ZashiButton("Rebuild Keystone Request", type: .ghost) {
                        store.send(.retryKeystoneSigning)
                    }
                }

            case .parsingSignature:
                ZashiButton("Processing Keystone signature...") { }
                    .disabled(true)
                    .opacity(0.5)

            case .failed:
                VStack(spacing: 8) {
                    ZashiButton("Retry Keystone Request") {
                        store.send(.retryKeystoneSigning)
                    }
                    ZashiButton("Scan Signed Keystone QR", type: .ghost) {
                        store.send(.openKeystoneSignatureScan)
                    }
                }
            }
        } else {
            ZashiButton("Authorize Voting") {
                store.send(.delegationApproved)
            }
            .disabled(!witnessReady)
            .opacity(witnessReady ? 1.0 : 0.5)
        }
    }

    // MARK: - Keystone

    @ViewBuilder
    private func keystoneSigningSection() -> some View {
        VStack(alignment: .leading, spacing: 10) {
            switch store.keystoneSigningStatus {
            case .idle:
                Text("Generate the delegation signing request to continue on Keystone.")
                    .zFont(size: 12, style: Design.Text.tertiary)
            case .preparingRequest:
                HStack(spacing: 8) {
                    ProgressView()
                    Text("Building 1-zatoshi delegation PCZT for Keystone...")
                        .zFont(.medium, size: 13, style: Design.Text.tertiary)
                }
            case .awaitingSignature:
                if let pczt = store.pendingUnsignedDelegationPczt,
                   let encoder = sdkSynchronizer.urEncoderForPCZT(pczt) {
                    AnimatedQRCode(urEncoder: encoder, size: 240)
                        .frame(maxWidth: .infinity, alignment: .center)
                        .padding(10)
                        .background {
                            RoundedRectangle(cornerRadius: 12)
                                .fill(Asset.Colors.ZDesign.Base.bone.color)
                        }
                    Text("Scan this QR in Keystone, sign the request, then scan the signed QR back.")
                        .zFont(size: 12, style: Design.Text.tertiary)
                } else {
                    Text("Keystone request is ready, but QR encoding failed. Rebuild the request.")
                        .zFont(size: 12, style: Design.Text.tertiary)
                }
            case .parsingSignature:
                HStack(spacing: 8) {
                    ProgressView()
                    Text("Extracting SpendAuthSig from signed PCZT...")
                        .zFont(.medium, size: 13, style: Design.Text.tertiary)
                }
            case .failed(let error):
                Text("Keystone signing failed: \(error)")
                    .zFont(size: 12, style: Design.Text.tertiary)
            }
        }
    }

}
