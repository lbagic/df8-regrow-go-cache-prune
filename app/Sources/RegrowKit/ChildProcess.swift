import Foundation

/// How long each rung of the stop ladder waits for the child to go.
public struct StopTimings: Sendable {
    public var stdin: TimeInterval = 5
    /// An execute in flight finishes the action it is running first.
    public var stdinDuringExecute: TimeInterval = 30
    public var term: TimeInterval = 5
    public var secondTerm: TimeInterval = 2
    public var kill: TimeInterval = 5

    public init() {}

    public init(stdin: TimeInterval, term: TimeInterval, secondTerm: TimeInterval) {
        self.stdin = stdin
        self.stdinDuringExecute = stdin
        self.term = term
        self.secondTerm = secondTerm
    }

    public static let standard = StopTimings()
}

/// The rung of the stop ladder that ended the child.
public enum StopRung: String, Sendable {
    case stdinClosed = "the end of stdin"
    case terminated = "SIGTERM"
    case terminatedTwice = "a second SIGTERM"
    case killed = "SIGKILL"
}

/// A child process: its stdout as lines, its stdin, its exit.
///
/// The child stays unreaped until its exit has been seen, and reaping
/// and signalling share one lock, so a signal only ever reaches the pid
/// while it still names this child.
public final class ChildProcess: @unchecked Sendable {
    public struct Exit: Equatable, Sendable, CustomStringConvertible {
        /// The exit status, nil when a signal ended the child.
        public let status: Int32?
        public let signal: Int32?

        init(waitStatus: Int32) {
            let low = waitStatus & 0x7F
            if low == 0 {
                status = (waitStatus >> 8) & 0xFF
                signal = nil
            } else {
                status = nil
                signal = low
            }
        }

        public var description: String {
            if let signal {
                return "was ended by signal \(signal)"
            }
            return "exited with status \(status ?? -1)"
        }
    }

    public let pid: pid_t

    private let state = NSCondition()
    private var reaped = false
    private var input: Int32
    private let writes = DispatchQueue(label: "regrow.child.stdin")
    private let exits = DispatchQueue(label: "regrow.child.exit")
    private var exitSource: DispatchSourceProcess?
    private let readerDone = DispatchSemaphore(value: 0)
    private let queue: DispatchQueue
    private let onExit: (Exit) -> Void

    /// Starts `executable` in a process group of its own. `onLine` gets
    /// each stdout line without its newline and `onExit` comes after
    /// the last of them, both on `queue`. stderr is appended to
    /// `errorLog`.
    public static func start(
        _ executable: String, arguments: [String], environment: [String: String],
        errorLog: URL?, queue: DispatchQueue,
        onLine: @escaping (Data) -> Void, onExit: @escaping (Exit) -> Void
    ) throws -> ChildProcess {
        var opened: [Int32] = []
        defer { opened.forEach { close($0) } }

        let stdin = try Spawn.pipe()
        opened += [stdin.read, stdin.write]
        let stdout = try Spawn.pipe()
        opened += [stdout.read, stdout.write]

        let errors: Int32
        if let errorLog {
            try? FileManager.default.createDirectory(
                at: errorLog.deletingLastPathComponent(), withIntermediateDirectories: true)
            errors = open(errorLog.path, O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC, 0o644)
        } else {
            errors = open("/dev/null", O_WRONLY | O_CLOEXEC)
        }
        guard errors >= 0 else {
            let path = errorLog?.path ?? "/dev/null"
            throw Spawn.Failure(description: "open \(path): \(String(cString: strerror(errno)))")
        }
        opened.append(errors)

        // A write to a child that has gone must fail, not raise SIGPIPE.
        _ = fcntl(stdin.write, F_SETNOSIGPIPE, 1)

        let pid = try Spawn.start(
            executable, arguments: arguments, environment: environment,
            stdin: stdin.read, stdout: stdout.write, stderr: errors, group: .own)

        opened.removeAll { $0 == stdin.write || $0 == stdout.read }
        let child = ChildProcess(pid: pid, input: stdin.write, queue: queue, onExit: onExit)
        child.read(stdout.read, onLine: onLine)
        child.watchExit()
        return child
    }

    private init(pid: pid_t, input: Int32, queue: DispatchQueue, onExit: @escaping (Exit) -> Void) {
        self.pid = pid
        self.input = input
        self.queue = queue
        self.onExit = onExit
    }

    private func read(_ descriptor: Int32, onLine: @escaping (Data) -> Void) {
        let queue = self.queue
        let done = readerDone
        Thread.detachNewThread {
            var framer = LineFramer()
            let size = 64 << 10
            let buffer = UnsafeMutableRawPointer.allocate(byteCount: size, alignment: 1)
            defer { buffer.deallocate() }
            while true {
                let n = Darwin.read(descriptor, buffer, size)
                if n > 0 {
                    for line in framer.append(Data(bytes: buffer, count: n)) {
                        queue.async { onLine(line) }
                    }
                } else if n == 0 || errno != EINTR {
                    break
                }
            }
            close(descriptor)
            done.signal()
        }
    }

    private func watchExit() {
        let source = DispatchSource.makeProcessSource(identifier: pid, eventMask: .exit, queue: exits)
        source.setEventHandler { self.reap(blocking: true) }
        exitSource = source
        source.resume()
        // The child may have gone before the source was armed.
        exits.async { self.reap(blocking: false) }
    }

    private func reap(blocking: Bool) {
        state.lock()
        if reaped {
            state.unlock()
            return
        }
        var status: Int32 = 0
        var result: pid_t
        repeat {
            result = waitpid(pid, &status, blocking ? 0 : WNOHANG)
        } while result == -1 && errno == EINTR
        guard result == pid else {
            state.unlock()
            return
        }
        reaped = true
        state.broadcast()
        state.unlock()

        exitSource?.cancel()
        exitSource = nil
        // Its last lines are still in the pipe. A grandchild that holds
        // the pipe open must not hold the exit back.
        _ = readerDone.wait(timeout: .now() + 1)
        closeStdin()
        let exit = Exit(waitStatus: status)
        queue.async { self.onExit(exit) }
    }

    /// Writes one request line. A line for a child that has gone is
    /// dropped.
    public func send(_ line: String) {
        writes.async { [self] in
            guard input >= 0 else { return }
            var bytes = Array(line.utf8)
            bytes.append(0x0A)
            var offset = 0
            while offset < bytes.count {
                let n = bytes.withUnsafeBytes {
                    write(input, $0.baseAddress! + offset, bytes.count - offset)
                }
                if n > 0 {
                    offset += n
                } else if errno != EINTR {
                    return
                }
            }
        }
    }

    public func closeStdin() {
        writes.async { [self] in
            if input >= 0 {
                close(input)
                input = -1
            }
        }
    }

    public var isRunning: Bool {
        state.lock()
        defer { state.unlock() }
        return !reaped
    }

    /// Signals the child; false when it had already gone.
    @discardableResult
    public func signal(_ signal: Int32) -> Bool {
        state.lock()
        defer { state.unlock() }
        guard !reaped else { return false }
        return kill(pid, signal) == 0
    }

    /// True once the child has gone, false when `timeout` passed first.
    public func waitForExit(timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        state.lock()
        defer { state.unlock() }
        while !reaped {
            if !state.wait(until: deadline) {
                break
            }
        }
        return reaped
    }

    /// The stop ladder: the end of stdin, SIGTERM, a second SIGTERM,
    /// SIGKILL, each after the one before it was given its time.
    /// Blocks until the child has gone or the last wait ran out.
    public func stop(executeInFlight: Bool, timings: StopTimings = .standard) -> StopRung {
        closeStdin()
        if waitForExit(timeout: executeInFlight ? timings.stdinDuringExecute : timings.stdin) {
            return .stdinClosed
        }
        signal(SIGTERM)
        if waitForExit(timeout: timings.term) {
            return .terminated
        }
        signal(SIGTERM)
        if waitForExit(timeout: timings.secondTerm) {
            return .terminatedTwice
        }
        signal(SIGKILL)
        _ = waitForExit(timeout: timings.kill)
        return .killed
    }
}
