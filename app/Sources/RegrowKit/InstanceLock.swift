import Foundation

/// `app.lock` in the state directory, held for as long as the app runs:
/// a second copy of the app on the same state directory finds it taken,
/// whatever path that copy runs from. The kernel lets it go when the
/// process ends, however it ends.
public final class InstanceLock {
    public static let name = "app.lock"

    public struct Failure: Error, CustomStringConvertible {
        public let description: String
    }

    private let descriptor: Int32

    private init(descriptor: Int32) {
        self.descriptor = descriptor
    }

    deinit {
        close(descriptor)
    }

    /// The lock, or nil when another process holds it.
    public static func acquire(in directory: URL) throws -> InstanceLock? {
        let path = directory.appendingPathComponent(name).path
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        } catch {
            throw Failure(description: "create \(directory.path): \(error.localizedDescription)")
        }
        // `flock` needs no write access, so a read-only file still locks.
        let descriptor = open(path, O_RDONLY | O_CREAT | O_CLOEXEC, 0o644)
        guard descriptor >= 0 else {
            throw Failure(description: "open \(path): \(String(cString: strerror(errno)))")
        }
        guard flock(descriptor, LOCK_EX | LOCK_NB) == 0 else {
            let code = errno
            close(descriptor)
            if code == EWOULDBLOCK {
                return nil
            }
            throw Failure(description: "lock \(path): \(String(cString: strerror(code)))")
        }
        return InstanceLock(descriptor: descriptor)
    }
}
