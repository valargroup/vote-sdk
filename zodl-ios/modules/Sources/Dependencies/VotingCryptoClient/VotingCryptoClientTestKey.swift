import ComposableArchitecture
import Foundation
import VotingModels

extension VotingCryptoClient: TestDependencyKey {
    public static let testValue = Self()
}
