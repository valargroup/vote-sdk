import Foundation

public struct VotingRound: Equatable {
    public let id: String
    public let title: String
    public let description: String
    public let snapshotHeight: UInt64
    public let snapshotDate: Date
    public let votingStart: Date
    public let votingEnd: Date
    public let proposals: [Proposal]

    public init(
        id: String,
        title: String,
        description: String,
        snapshotHeight: UInt64,
        snapshotDate: Date,
        votingStart: Date,
        votingEnd: Date,
        proposals: [Proposal]
    ) {
        self.id = id
        self.title = title
        self.description = description
        self.snapshotHeight = snapshotHeight
        self.snapshotDate = snapshotDate
        self.votingStart = votingStart
        self.votingEnd = votingEnd
        self.proposals = proposals
    }

    public var daysRemaining: Int {
        Calendar.current.dateComponents([.day], from: Date(), to: votingEnd).day ?? 0
    }
}
