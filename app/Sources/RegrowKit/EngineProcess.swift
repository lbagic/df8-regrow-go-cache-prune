import Foundation

/// The wait before an engine that exited is started again: it doubles
/// with each exit and starts over once an engine has stayed up.
public struct RestartDelay {
    public static let longest: TimeInterval = 60
    public static let settledAfter: TimeInterval = 60

    private var next: TimeInterval = 1

    public init() {}

    public mutating func after(uptime: TimeInterval) -> TimeInterval {
        if uptime >= Self.settledAfter {
            next = 1
        }
        let delay = next
        next = min(next * 2, Self.longest)
        return delay
    }
}

/// Keeps one engine process running: starts it, hands up its stdout
/// lines, reports an exit nobody asked for and starts it again.
public final class EngineSupervisor: @unchecked Sendable {
    public struct Launch {
        public var executable: String
        public var arguments: [String]
        public var environment: [String: String]
        /// The engine's stderr is appended here.
        public var log: URL

        public init(executable: String, arguments: [String], environment: [String: String], log: URL) {
            self.executable = executable
            self.arguments = arguments
            self.environment = environment
            self.log = log
        }
    }

    /// An exit nobody asked for, or a start that failed.
    public struct Down {
        public let reason: String
        /// What this run of the engine left at the end of its log.
        public let logTail: String
        public let retryIn: TimeInterval

        public var message: String {
            logTail.isEmpty ? reason : "\(reason): \(logTail)"
        }
    }

    private let launch: Launch
    private let queue: DispatchQueue
    private let onStart: (pid_t) -> Void
    private let onLine: (Data) -> Void
    private let onDown: (Down) -> Void

    private let lock = NSLock()
    private var child: ChildProcess?
    private var stopping = false
    private var retry: DispatchWorkItem?

    private var delay = RestartDelay()
    private var startedAt = Date()
    private var logOffset: UInt64 = 0

    /// Every callback comes on `queue`.
    public init(
        launch: Launch, queue: DispatchQueue,
        onStart: @escaping (pid_t) -> Void,
        onLine: @escaping (Data) -> Void,
        onDown: @escaping (Down) -> Void
    ) {
        self.launch = launch
        self.queue = queue
        self.onStart = onStart
        self.onLine = onLine
        self.onDown = onDown
    }

    public func start() {
        queue.async { self.spawn() }
    }

    public var pid: pid_t? {
        lock.lock()
        defer { lock.unlock() }
        return child?.pid
    }

    public func send(_ line: String) {
        lock.lock()
        let child = self.child
        lock.unlock()
        child?.send(line)
    }

    /// Runs the stop ladder on the engine and starts no other. Blocks;
    /// nil when no engine was running.
    public func stop(executeInFlight: Bool, timings: StopTimings = .standard) -> StopRung? {
        lock.lock()
        stopping = true
        retry?.cancel()
        let child = self.child
        lock.unlock()
        return child?.stop(executeInFlight: executeInFlight, timings: timings)
    }

    private func spawn() {
        // The lock covers the start, so a stop never misses an engine
        // that was just being started.
        lock.lock()
        guard !stopping else {
            lock.unlock()
            return
        }
        logOffset = Self.size(of: launch.log)
        startedAt = Date()
        let started = Result {
            try ChildProcess.start(
                launch.executable, arguments: launch.arguments, environment: launch.environment,
                errorLog: launch.log, queue: queue,
                onLine: onLine,
                onExit: { [weak self] exit in self?.exited(exit) })
        }
        if case .success(let child) = started {
            self.child = child
        }
        lock.unlock()

        switch started {
        case .success(let child):
            onStart(child.pid)
        case .failure(let error):
            down(reason: "\(error)", logTail: "")
        }
    }

    private func exited(_ exit: ChildProcess.Exit) {
        lock.lock()
        child = nil
        let expected = stopping
        lock.unlock()
        if !expected {
            down(reason: "the engine \(exit)", logTail: tail())
        }
    }

    private func down(reason: String, logTail: String) {
        let retryIn = delay.after(uptime: Date().timeIntervalSince(startedAt))
        onDown(Down(reason: reason, logTail: logTail, retryIn: retryIn))

        let again = DispatchWorkItem { [weak self] in self?.spawn() }
        lock.lock()
        let stopped = stopping
        if !stopped {
            retry = again
        }
        lock.unlock()
        if !stopped {
            queue.asyncAfter(deadline: .now() + retryIn, execute: again)
        }
    }

    private static func size(of file: URL) -> UInt64 {
        let attributes = try? FileManager.default.attributesOfItem(atPath: file.path)
        return (attributes?[.size] as? NSNumber)?.uint64Value ?? 0
    }

    private static let tailBytes: UInt64 = 4 << 10

    private func tail() -> String {
        guard let handle = try? FileHandle(forReadingFrom: launch.log) else { return "" }
        defer { try? handle.close() }
        let size = Self.size(of: launch.log)
        let from = max(logOffset, size > Self.tailBytes ? size - Self.tailBytes : 0)
        guard from < size, (try? handle.seek(toOffset: from)) != nil,
            let data = try? handle.readToEnd()
        else { return "" }
        return String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
