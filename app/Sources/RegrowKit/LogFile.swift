import Foundation

/// An append-only log, one timestamped line per fact.
public final class LogFile: @unchecked Sendable {
    public let url: URL
    private let lock = NSLock()
    private var handle: FileHandle?
    private let stamp: ISO8601DateFormatter

    public init(url: URL) {
        self.url = url
        stamp = ISO8601DateFormatter()
        stamp.timeZone = .current
        stamp.formatOptions = [.withInternetDateTime]
    }

    public func callAsFunction(_ text: String) {
        lock.lock()
        defer { lock.unlock() }
        if handle == nil {
            try? FileManager.default.createDirectory(
                at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            let descriptor = open(url.path, O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC, 0o644)
            if descriptor >= 0 {
                handle = FileHandle(fileDescriptor: descriptor, closeOnDealloc: true)
            }
        }
        let line = "\(stamp.string(from: Date())) \(text.replacingOccurrences(of: "\n", with: " ⏎ "))\n"
        try? handle?.write(contentsOf: Data(line.utf8))
    }
}
