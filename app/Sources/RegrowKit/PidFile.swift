import Foundation

/// `app.pid` in the state directory: the app's pid on the first line,
/// its engine's on the second. It is how an engine that outlived its
/// app is found again.
public struct PidFile: Equatable {
    public static let name = "app.pid"

    public var app: pid_t
    public var engine: pid_t

    public init(app: pid_t, engine: pid_t) {
        self.app = app
        self.engine = engine
    }

    /// The state directory as the engine started with `environment`
    /// resolves it, so the app, the engine and a script in a terminal
    /// all mean the same file.
    public static func stateDirectory(environment: [String: String]) -> URL {
        if let state = environment["XDG_STATE_HOME"], !state.isEmpty {
            return URL(fileURLWithPath: state).appendingPathComponent("regrow")
        }
        let home = environment["HOME"].flatMap { $0.isEmpty ? nil : $0 } ?? NSHomeDirectory()
        return URL(fileURLWithPath: home).appendingPathComponent(".local/state/regrow")
    }

    public func write(to directory: URL) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try Data("\(app)\n\(engine)\n".utf8)
            .write(to: directory.appendingPathComponent(Self.name), options: .atomic)
    }

    public static func read(from directory: URL) -> PidFile? {
        guard let text = try? String(contentsOf: directory.appendingPathComponent(name), encoding: .utf8)
        else { return nil }
        let pids = text.split(separator: "\n").map { pid_t($0.trimmingCharacters(in: .whitespaces)) }
        guard pids.count == 2, let app = pids[0], let engine = pids[1], app > 0, engine > 0 else {
            return nil
        }
        return PidFile(app: app, engine: engine)
    }

    public static func remove(from directory: URL) {
        try? FileManager.default.removeItem(at: directory.appendingPathComponent(name))
    }
}

/// A process this app did not start, known only by a pid from an
/// earlier run. A pid can have passed to any other program since, so it
/// is signalled only while its executable is the one expected.
public enum EarlierProcess {
    public enum Outcome: Equatable {
        /// No process has the pid, or one of another executable has.
        case notOurs
        case stopped(StopRung)
        /// Still there after SIGKILL was given its time.
        case survived
    }

    /// The executable of `pid`, nil when no such process is running.
    public static func executable(of pid: pid_t) -> String? {
        var buffer = [CChar](repeating: 0, count: 4 * Int(MAXPATHLEN))
        guard proc_pidpath(pid, &buffer, UInt32(buffer.count)) > 0 else { return nil }
        return String(cString: buffer)
    }

    public static func isRunning(_ pid: pid_t, executable: String) -> Bool {
        guard let path = self.executable(of: pid) else { return false }
        return resolved(path) == resolved(executable)
    }

    /// The stop ladder from its first SIGTERM: stdin is not ours to
    /// close. Nothing says whether the engine is running an action, which
    /// the second SIGTERM would cut short, so the first gets the wait an
    /// execute in flight gets.
    public static func stop(_ pid: pid_t, executable: String, timings: StopTimings = .standard) -> Outcome {
        guard isRunning(pid, executable: executable) else { return .notOurs }
        let rungs: [(Int32, TimeInterval, StopRung)] = [
            (SIGTERM, max(timings.term, timings.stdinDuringExecute), .terminated),
            (SIGTERM, timings.secondTerm, .terminatedTwice),
            (SIGKILL, timings.kill, .killed),
        ]
        var sent = StopRung.terminated
        for (signal, wait, rung) in rungs {
            guard isRunning(pid, executable: executable) else { return .stopped(sent) }
            sent = rung
            kill(pid, signal)
            let deadline = Date().addingTimeInterval(wait)
            while isRunning(pid, executable: executable), Date() < deadline {
                usleep(50_000)
            }
        }
        return isRunning(pid, executable: executable) ? .survived : .stopped(sent)
    }

    private static func resolved(_ path: String) -> String {
        URL(fileURLWithPath: path).resolvingSymlinksInPath().path
    }
}
