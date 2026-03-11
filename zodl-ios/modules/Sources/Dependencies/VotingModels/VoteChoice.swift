public enum VoteChoice: Equatable, Hashable, Sendable {
    case option(UInt32)

    public var index: UInt32 {
        switch self {
        case .option(let i):
            return i
        }
    }
}

// Custom Codable: encodes/decodes as a plain UInt32 for backwards-compatible DB storage.
extension VoteChoice: Codable {
    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        let value = try container.decode(UInt32.self)
        self = .option(value)
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(index)
    }
}
