import XCTest
import ComposableArchitecture
import Voting
import VotingModels
@testable import secant_testnet

@MainActor
final class VotingStoreTests: XCTestCase {
    func testNonKeystoneDelegationApprovalSkipsDelegationPipeline() async throws {
        let store = TestStore(initialState: Voting.State(
            votingRound: MockVotingService.votingRound,
            votingWeight: 1,
            isKeystoneUser: false,
            roundId: "aabb"
        )) {
            Voting()
        }

        await store.send(.delegationApproved) { state in
            state.screenStack = [.proposalList]
            state.delegationProofStatus = .complete
        }
    }

    func testKeystoneSignatureResumesDelegationProofPipeline() async throws {
        var initialState = Voting.State(
            votingRound: MockVotingService.votingRound,
            votingWeight: 1,
            isKeystoneUser: true,
            roundId: "aabb"
        )
        initialState.activeSession = VotingSession(
            voteRoundId: Data(repeating: 0xAA, count: 32),
            snapshotHeight: 1,
            snapshotBlockhash: Data(repeating: 0x01, count: 32),
            proposalsHash: Data(repeating: 0x02, count: 32),
            voteEndTime: Date(),
            eaPK: Data(repeating: 0x03, count: 32),
            vkZkp1: Data(repeating: 0x04, count: 32),
            vkZkp2: Data(repeating: 0x05, count: 32),
            vkZkp3: Data(repeating: 0x06, count: 32),
            ncRoot: Data(repeating: 0x07, count: 32),
            nullifierIMTRoot: Data(repeating: 0x08, count: 32),
            creator: "creator",
            proposals: MockVotingService.votingRound.proposals,
            status: .active
        )
        let mockGovPczt = GovernancePcztResult(
            pcztBytes: Data(repeating: 0xAB, count: 128),
            rk: Data(repeating: 0x11, count: 32),
            alpha: Data(repeating: 0x1A, count: 32),
            nfSigned: Data(repeating: 0x18, count: 32),
            cmxNew: Data(repeating: 0x19, count: 32),
            govNullifiers: [Data](repeating: Data(repeating: 0x15, count: 32), count: 4),
            van: Data(repeating: 0x16, count: 32),
            govCommRand: Data(repeating: 0x03, count: 32),
            dummyNullifiers: [],
            rhoSigned: Data(repeating: 0x04, count: 32),
            paddedCmx: [],
            rseedSigned: Data(repeating: 0x1B, count: 32),
            rseedOutput: Data(repeating: 0x1C, count: 32),
            actionBytes: Data(repeating: 0x10, count: 64),
            actionIndex: 0
        )
        initialState.pendingGovernancePczt = mockGovPczt
        initialState.pendingDelegationAction = mockGovPczt.toDelegationAction()
        initialState.pendingUnsignedDelegationPczt = Data(repeating: 0xAB, count: 128)

        let store = TestStore(initialState: initialState) {
            Voting()
        }

        store.dependencies.votingCrypto.buildDelegationWitness = { _, _, _, _, _ in
            Data(repeating: 0xEE, count: 32)
        }
        store.dependencies.votingCrypto.generateDelegationProof = { _ in
            AsyncThrowingStream { continuation in
                continuation.yield(.progress(1.0))
                continuation.yield(.completed(Data(repeating: 0xFF, count: 32)))
                continuation.finish()
            }
        }

        await store.send(.spendAuthSignatureExtracted(Data(repeating: 0x44, count: 64))) { state in
            state.pendingDelegationAction = nil
            state.pendingGovernancePczt = nil
            state.pendingUnsignedDelegationPczt = nil
            state.keystoneSigningStatus = .idle
            state.screenStack = [.proposalList]
            state.delegationProofStatus = .generating(progress: 0)
        }

        await store.receive(.delegationProofProgress(1.0)) { state in
            state.delegationProofStatus = .generating(progress: 1.0)
        }
        await store.receive(.delegationProofCompleted) { state in
            state.delegationProofStatus = .complete
        }
    }

    // Note: Client-side signature verification was removed — Keystone signatures
    // are correct by construction (signed over the PCZT's ZIP-244 sighash).
    // Verification will be re-added once the GovernancePczt flow is fully wired (Task #6).
}
