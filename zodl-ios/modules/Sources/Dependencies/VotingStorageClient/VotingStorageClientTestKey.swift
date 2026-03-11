import ComposableArchitecture
import Foundation
import VotingModels

extension VotingStorageClient: TestDependencyKey {
    public static let testValue = Self()
}
