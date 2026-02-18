import Foundation

/// A single vote option within a proposal (e.g. "Support", "Oppose").
/// Maps to VoteOption message (zvote/v1/types.proto).
public struct VoteOption: Equatable, Sendable {
    public let index: UInt32
    public let label: String

    public init(index: UInt32, label: String) {
        self.index = index
        self.label = label
    }
}

/// Maps to Proposal message (zvote/v1/types.proto).
/// Chain uses uint32 id. UI-only metadata (zipNumber, forumURL) comes from off-chain sources.
public struct Proposal: Equatable, Identifiable, Sendable {
    public let id: UInt32
    public let title: String
    public let description: String
    public let options: [VoteOption]
    public let zipNumber: String?
    public let forumURL: URL?

    public init(
        id: UInt32,
        title: String,
        description: String,
        options: [VoteOption] = [],
        zipNumber: String? = nil,
        forumURL: URL? = nil
    ) {
        self.id = id
        self.title = title
        self.description = description
        self.options = options
        self.zipNumber = zipNumber
        self.forumURL = forumURL
    }
}
