import Foundation
import Utils

public struct StoredVotingHotkey: Codable, Equatable {
    public let seedPhrase: SeedPhrase
    public let version: Int

    public init(seedPhrase: SeedPhrase, version: Int) {
        self.seedPhrase = seedPhrase
        self.version = version
    }
}
