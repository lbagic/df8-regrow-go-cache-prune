import Foundation

/// Starts a child with exactly three descriptors and default signals.
public enum Spawn {
    public struct Failure: Error, CustomStringConvertible {
        public let description: String
    }

    public enum Group {
        /// The parent's process group.
        case inherited
        /// A process group of its own: a signal sent to the parent's
        /// group, such as a terminal's interrupt, does not reach it.
        case own
        /// A session of its own, so no controlling terminal: an
        /// interactive shell in a background group of a terminal's
        /// session would stop itself waiting for the foreground.
        case session
    }

    /// The child sees `stdin`, `stdout` and `stderr` as 0, 1 and 2 and
    /// no other descriptor of the parent. A signal the parent ignores
    /// would stay ignored across exec, so every disposition is reset.
    public static func start(
        _ executable: String, arguments: [String], environment: [String: String],
        stdin: Int32, stdout: Int32, stderr: Int32, group: Group = .inherited
    ) throws -> pid_t {
        var actions: posix_spawn_file_actions_t?
        posix_spawn_file_actions_init(&actions)
        defer { posix_spawn_file_actions_destroy(&actions) }
        posix_spawn_file_actions_adddup2(&actions, stdin, 0)
        posix_spawn_file_actions_adddup2(&actions, stdout, 1)
        posix_spawn_file_actions_adddup2(&actions, stderr, 2)

        var attributes: posix_spawnattr_t?
        posix_spawnattr_init(&attributes)
        defer { posix_spawnattr_destroy(&attributes) }
        var flags = POSIX_SPAWN_CLOEXEC_DEFAULT | POSIX_SPAWN_SETSIGDEF | POSIX_SPAWN_SETSIGMASK
        switch group {
        case .inherited:
            break
        case .own:
            flags |= POSIX_SPAWN_SETPGROUP
            posix_spawnattr_setpgroup(&attributes, 0)
        case .session:
            flags |= POSIX_SPAWN_SETSID
        }
        var every = sigset_t()
        sigfillset(&every)
        posix_spawnattr_setsigdefault(&attributes, &every)
        var none = sigset_t()
        sigemptyset(&none)
        posix_spawnattr_setsigmask(&attributes, &none)
        posix_spawnattr_setflags(&attributes, Int16(flags))

        var argv: [UnsafeMutablePointer<CChar>?] = ([executable] + arguments).map { strdup($0) }
        argv.append(nil)
        var envp: [UnsafeMutablePointer<CChar>?] = environment.map { strdup("\($0.key)=\($0.value)") }
        envp.append(nil)
        defer {
            argv.forEach { free($0) }
            envp.forEach { free($0) }
        }

        var pid: pid_t = 0
        let code = posix_spawn(&pid, executable, &actions, &attributes, argv, envp)
        guard code == 0 else {
            throw Failure(description: "could not start \(executable): \(String(cString: strerror(code)))")
        }
        return pid
    }

    /// A pipe whose ends are closed in every child that is not handed
    /// one: a stray copy of a write end would keep its reader from ever
    /// seeing the end.
    static func pipe() throws -> (read: Int32, write: Int32) {
        var ends: [Int32] = [-1, -1]
        guard Darwin.pipe(&ends) == 0 else {
            throw Failure(description: "pipe: \(String(cString: strerror(errno)))")
        }
        for end in ends {
            _ = fcntl(end, F_SETFD, FD_CLOEXEC)
        }
        return (ends[0], ends[1])
    }
}
