import Foundation
import RegrowKit

/// Whether regrow may control Finder, which a Trash move needs.
enum Automation {
    private static let question = "tell application \"Finder\" to get name of startup disk"

    /// Asks Finder one harmless question. The first time, macOS asks
    /// the owner, and the answer waits for as long as they read the
    /// dialog: there is no time limit. `answer` is called on `queue`.
    static func askFinder(queue: DispatchQueue, _ answer: @escaping (Bool) -> Void) {
        do {
            _ = try ChildProcess.start(
                "/usr/bin/osascript", arguments: ["-e", question],
                environment: Account.environment(), errorLog: nil, queue: queue,
                onLine: { _ in },
                onExit: { exit in answer(exit.status == 0) })
        } catch {
            queue.async { answer(false) }
        }
    }
}
