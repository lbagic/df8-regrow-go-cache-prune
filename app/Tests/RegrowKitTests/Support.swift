import Foundation
import RegrowKit

/// A directory of the test's own under the system temp dir, removed
/// when `body` returns.
func withTemporaryDirectory<T>(_ body: (URL) async throws -> T) async throws -> T {
    let directory = FileManager.default.temporaryDirectory
        .appendingPathComponent("regrow-tests-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: directory) }
    return try await body(directory)
}

/// Writes an executable script and returns its path.
func writeScript(_ text: String, named name: String, in directory: URL) throws -> String {
    let url = directory.appendingPathComponent(name)
    try Data(text.utf8).write(to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
    return url.path
}

/// What a child process or a supervisor reported, collected from its
/// callback queue and awaited from a test.
final class Inbox<Element>: @unchecked Sendable {
    private let lock = NSLock()
    private var elements: [Element] = []

    func add(_ element: Element) {
        lock.lock()
        elements.append(element)
        lock.unlock()
    }

    var all: [Element] {
        lock.lock()
        defer { lock.unlock() }
        return elements
    }

    /// The elements once `condition` holds of them, or nil after
    /// `timeout` seconds.
    func wait(_ timeout: TimeInterval, until condition: ([Element]) -> Bool) async -> [Element]? {
        let deadline = Date().addingTimeInterval(timeout)
        while true {
            let now = all
            if condition(now) {
                return now
            }
            if Date() >= deadline {
                return nil
            }
            try? await Task.sleep(for: .milliseconds(10))
        }
    }
}

/// Runs a blocking call off the cooperative pool.
func offThread<T: Sendable>(_ work: @escaping @Sendable () -> T) async -> T {
    await withCheckedContinuation { continuation in
        DispatchQueue.global().async {
            continuation.resume(returning: work())
        }
    }
}

let toolPath = ["PATH": "/usr/bin:/bin:/usr/sbin:/sbin"]
