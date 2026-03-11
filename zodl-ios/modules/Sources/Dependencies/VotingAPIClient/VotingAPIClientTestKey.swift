import ComposableArchitecture
import Foundation
import VotingModels

extension VotingAPIClient: TestDependencyKey {
    public static let testValue = Self()
}
