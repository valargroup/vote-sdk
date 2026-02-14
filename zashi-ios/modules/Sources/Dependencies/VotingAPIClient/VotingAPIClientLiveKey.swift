import ComposableArchitecture
import Foundation
import VotingModels

extension VotingAPIClient: DependencyKey {
    public static var liveValue: Self {
        Self(
            fetchActiveVotingSession: {
                // Stub: return mock session matching the prototype's VotingRound
                try await Task.sleep(for: .milliseconds(200))
                return VotingSession(
                    voteRoundId: Data(repeating: 0x0A, count: 32),
                    snapshotHeight: 3_235_467,
                    snapshotBlockhash: Data(repeating: 0x0B, count: 32),
                    proposalsHash: Data(repeating: 0x0C, count: 32),
                    voteEndTime: Calendar.current.date(byAdding: .day, value: 5, to: Date())!,
                    eaPK: Data(repeating: 0x01, count: 32),
                    vkZkp1: Data(repeating: 0x02, count: 32),
                    vkZkp2: Data(repeating: 0x03, count: 32),
                    vkZkp3: Data(repeating: 0x04, count: 32),
                    ncRoot: Data(repeating: 0x05, count: 32),
                    nullifierIMTRoot: Data(repeating: 0x06, count: 32),
                    creator: "zvote1admin",
                    proposals: [],
                    status: .active
                )
            },
            fetchVotingWeight: { _ in
                try await Task.sleep(for: .milliseconds(100))
                return 14_250_000_000 // 142.50 ZEC
            },
            fetchNoteInclusionProofs: { commitments in
                try await Task.sleep(for: .milliseconds(100))
                return commitments.map { _ in Data(repeating: 0x11, count: 32) }
            },
            fetchNullifierExclusionProofs: { nullifiers in
                try await Task.sleep(for: .milliseconds(100))
                return nullifiers.map { _ in Data(repeating: 0x22, count: 32) }
            },
            fetchCommitmentTreeState: { _ in
                try await Task.sleep(for: .milliseconds(100))
                return CommitmentTreeState(nextIndex: 1024, root: Data(repeating: 0x33, count: 32), height: 3_235_467)
            },
            fetchLatestCommitmentTree: {
                try await Task.sleep(for: .milliseconds(100))
                return CommitmentTreeState(nextIndex: 2048, root: Data(repeating: 0x44, count: 32), height: 2_800_100)
            },
            submitDelegation: { _ in
                try await Task.sleep(for: .milliseconds(500))
                return TxResult(txHash: "mock_delegation_tx_\(UUID().uuidString.prefix(8))", code: 0)
            },
            submitVoteCommitment: { _ in
                try await Task.sleep(for: .milliseconds(300))
                return TxResult(txHash: "mock_vote_tx_\(UUID().uuidString.prefix(8))", code: 0)
            },
            delegateShares: { _ in
                try await Task.sleep(for: .milliseconds(200))
            },
            fetchProposalTally: { _, _ in
                try await Task.sleep(for: .milliseconds(200))
                return TallyResult(entries: [
                    TallyResult.Entry(decision: 0, amount: 50_000_000_000),
                    TallyResult.Entry(decision: 1, amount: 30_000_000_000),
                    TallyResult.Entry(decision: 2, amount: 10_000_000_000)
                ])
            }
        )
    }
}
