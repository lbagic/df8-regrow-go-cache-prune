import Foundation

/// The environment a terminal would give the user: an app started by
/// LaunchServices has only the system folders on its PATH.
public enum LoginEnvironment {
    public struct Failure: Error, Equatable, CustomStringConvertible {
        public let reason: String

        public init(reason: String) {
            self.reason = reason
        }

        public var description: String { reason }
    }

    /// The user's shell from the password database: `$SHELL` is whatever
    /// the launching process was given.
    public static func loginShell() -> String {
        if let entry = getpwuid(getuid()), let shell = entry.pointee.pw_shell {
            let path = String(cString: shell)
            if !path.isEmpty {
                return path
            }
        }
        return "/bin/zsh"
    }

    /// Runs `shell` as a login, interactive shell that prints its
    /// environment between two markers, and gives up after `timeout`.
    public static func capture(
        shell: String = loginShell(), timeout: TimeInterval = 5
    ) -> Result<[String: String], Failure> {
        let token = UUID().uuidString
        let opening = "regrow-environment-\(token)-begins"
        let closing = "regrow-environment-\(token)-ends"
        let command = "printf '%s' '\(opening)'; /usr/bin/env -0; printf '%s' '\(closing)'"

        let null = open("/dev/null", O_RDWR | O_CLOEXEC)
        guard null >= 0, let output = try? Spawn.pipe() else {
            if null >= 0 { close(null) }
            return .failure(Failure(reason: "could not open a pipe to \(shell)"))
        }
        let pid: pid_t
        do {
            pid = try Spawn.start(
                shell, arguments: ["-l", "-i", "-c", command],
                environment: ProcessInfo.processInfo.environment,
                stdin: null, stdout: output.write, stderr: null, group: .session)
        } catch {
            close(null)
            close(output.read)
            close(output.write)
            return .failure(Failure(reason: "\(error)"))
        }
        close(null)
        close(output.write)
        defer { close(output.read) }

        let deadline = Date().addingTimeInterval(timeout)
        let (printed, complete) = read(output.read, until: Data(closing.utf8), deadline: deadline)
        let status = reap(pid, deadline: deadline)

        switch parse(printed, opening: opening, closing: closing) {
        case .success(let environment):
            return .success(environment)
        case .failure(let failure):
            if !complete, status == nil {
                return .failure(Failure(reason: "\(shell) did not print its environment within \(Int(timeout)) seconds"))
            }
            if let status, status != 0 {
                return .failure(Failure(reason: "\(shell) ended with status \(status) before it printed its environment"))
            }
            return .failure(failure)
        }
    }

    /// The variables between the markers. Whatever a profile printed
    /// before or after them is ignored.
    public static func parse(
        _ output: Data, opening: String, closing: String
    ) -> Result<[String: String], Failure> {
        guard let start = output.range(of: Data(opening.utf8)) else {
            return .failure(Failure(reason: "the shell's output has no environment in it"))
        }
        guard let end = output.range(of: Data(closing.utf8), in: start.upperBound..<output.endIndex) else {
            return .failure(Failure(reason: "the shell's output ended before its environment did"))
        }
        var environment: [String: String] = [:]
        for entry in output[start.upperBound..<end.lowerBound].split(separator: 0) {
            guard let equals = entry.firstIndex(of: UInt8(ascii: "=")), equals > entry.startIndex,
                let name = String(data: entry[entry.startIndex..<equals], encoding: .utf8),
                let value = String(data: entry[entry.index(after: equals)...], encoding: .utf8)
            else { continue }
            environment[name] = value
        }
        return .success(environment)
    }

    /// Reads until `marker` has been seen, the stream ends or the
    /// deadline passes. A child of the shell can hold the pipe open
    /// long after the shell has printed everything.
    private static func read(_ descriptor: Int32, until marker: Data, deadline: Date) -> (Data, complete: Bool) {
        var printed = Data()
        var buffer = [UInt8](repeating: 0, count: 64 << 10)
        while true {
            let left = deadline.timeIntervalSinceNow
            if left <= 0 {
                return (printed, false)
            }
            var poller = pollfd(fd: descriptor, events: Int16(POLLIN), revents: 0)
            let ready = poll(&poller, 1, Int32(min(left * 1000, 200).rounded(.up)))
            if ready == 0 || (ready < 0 && errno == EINTR) {
                continue
            }
            if ready < 0 {
                return (printed, false)
            }
            let n = Darwin.read(descriptor, &buffer, buffer.count)
            if n > 0 {
                let searchFrom = max(printed.count - marker.count, 0)
                printed.append(buffer, count: n)
                if printed.range(of: marker, in: searchFrom..<printed.count) != nil {
                    return (printed, true)
                }
            } else if n == 0 {
                return (printed, true)
            } else if errno != EINTR && errno != EAGAIN {
                return (printed, false)
            }
        }
    }

    /// The shell's exit status. A shell still running at the deadline
    /// is killed with its whole process group, and nil is returned.
    private static func reap(_ pid: pid_t, deadline: Date) -> Int32? {
        var status: Int32 = 0
        while true {
            let result = waitpid(pid, &status, WNOHANG)
            if result == pid {
                return ChildProcess.Exit(waitStatus: status).status ?? -1
            }
            if result == -1 && errno != EINTR {
                return nil
            }
            if deadline.timeIntervalSinceNow <= 0 {
                break
            }
            usleep(10_000)
        }
        // The shell leads its own session, so its pid names its group,
        // and the pid cannot be reused before the shell is reaped.
        kill(-pid, SIGKILL)
        while waitpid(pid, &status, 0) == -1 && errno == EINTR {}
        return nil
    }
}
