import Foundation
import RegrowKit
import Testing

/// The children are shell scripts in a temp dir. Each one ends by
/// itself within 20 s whatever the test does, so none outlives a test
/// that failed.
private enum Children {
    static let loop = "i=0\nwhile [ $i -lt 200 ]; do sleep 0.1; i=$((i+1)); done\n"

    static let exitsOnStdinEnd = "#!/bin/bash\necho ready\nwhile read -t 20 line; do :; done\n"
    static let honoursSigterm = "#!/bin/sh\necho ready\n" + loop
    static let needsSecondSigterm =
        "#!/bin/bash\nn=0\ntrap 'n=$((n+1)); if [ $n -ge 2 ]; then exit 0; fi' TERM\necho ready\n" + loop
    static let ignoresBoth = "#!/bin/sh\ntrap '' TERM\necho ready\n" + loop
    /// As the engine runs an action: the first SIGTERM lets the action
    /// end (2 s) and the process exit 0; a second one kills it.
    static let finishesItsAction =
        "#!/bin/bash\ntrap 'trap - TERM; sleep 2; exit 0' TERM\necho ready\n" + loop
}

private let quick = StopTimings(stdin: 1, term: 1, secondTerm: 1)

private struct Run {
    let child: ChildProcess
    let lines: Inbox<String>
    let exits: Inbox<ChildProcess.Exit>
}

private func launch(_ script: String, in directory: URL, errorLog: URL? = nil) throws -> Run {
    let path = try writeScript(script, named: "child-\(UUID().uuidString)", in: directory)
    let lines = Inbox<String>()
    let exits = Inbox<ChildProcess.Exit>()
    let child = try ChildProcess.start(
        path, arguments: [], environment: toolPath, errorLog: errorLog,
        queue: DispatchQueue(label: "child-test"),
        onLine: { lines.add(String(decoding: $0, as: UTF8.self)) },
        onExit: { exits.add($0) })
    return Run(child: child, lines: lines, exits: exits)
}

@Suite("Stop ladder", .serialized)
struct StopLadderTests {
    private func stopped(_ script: String) async throws -> (StopRung, ChildProcess.Exit) {
        try await withTemporaryDirectory { directory in
            let run = try launch(script, in: directory)
            try #require(await run.lines.wait(5) { $0.contains("ready") } != nil)

            let child = run.child
            let rung = await offThread { child.stop(executeInFlight: false, timings: quick) }
            let exits = try #require(await run.exits.wait(5) { !$0.isEmpty })
            #expect(!child.isRunning)
            return (rung, exits[0])
        }
    }

    @Test("a child that exits when stdin ends is not signalled")
    func stdinEnd() async throws {
        let (rung, exit) = try await stopped(Children.exitsOnStdinEnd)
        #expect(rung == .stdinClosed)
        #expect(exit.signal == nil)
    }

    @Test("a child that ignores the end of stdin goes on SIGTERM")
    func sigterm() async throws {
        let (rung, exit) = try await stopped(Children.honoursSigterm)
        #expect(rung == .terminated)
        #expect(exit.signal == SIGTERM)
    }

    @Test("a child that takes the first SIGTERM as a request goes on the second")
    func secondSigterm() async throws {
        let (rung, exit) = try await stopped(Children.needsSecondSigterm)
        #expect(rung == .terminatedTwice)
        #expect(exit.status == 0)
    }

    @Test("a child that ignores both is killed")
    func sigkill() async throws {
        let (rung, exit) = try await stopped(Children.ignoresBoth)
        #expect(rung == .killed)
        #expect(exit.signal == SIGKILL)
    }

    @Test("an execute in flight is given its own, longer wait before the first signal")
    func executeInFlightWaitsLonger() async throws {
        try await withTemporaryDirectory { directory in
            // Ends 2 s after stdin does: past the plain wait, within
            // the one an execute gets.
            let script = "#!/bin/bash\necho ready\nwhile read -t 20 line; do :; done\nsleep 2\n"
            let run = try launch(script, in: directory)
            try #require(await run.lines.wait(5) { $0.contains("ready") } != nil)

            var longer = quick
            longer.stdinDuringExecute = 5
            let timings = longer
            let child = run.child
            let rung = await offThread { child.stop(executeInFlight: true, timings: timings) }
            #expect(rung == .stdinClosed)
        }
    }
}

@Suite("Child process")
struct ChildProcessTests {
    @Test("a 2 MiB line arrives whole, and the exit is reported after the last line")
    func longLineThenExit() async throws {
        try await withTemporaryDirectory { directory in
            let script = "#!/bin/sh\nhead -c 2097152 /dev/zero | tr '\\0' x\necho\necho last\nexit 3\n"
            let order = Inbox<String>()
            let path = try writeScript(script, named: "child", in: directory)
            _ = try ChildProcess.start(
                path, arguments: [], environment: toolPath, errorLog: nil,
                queue: DispatchQueue(label: "child-test"),
                onLine: { order.add($0.count == 2 << 20 ? "2 MiB of x" : String(decoding: $0, as: UTF8.self)) },
                onExit: { order.add("exit \($0.status ?? -1)") })

            let seen = await order.wait(10) { $0.count == 3 }
            #expect(seen == ["2 MiB of x", "last", "exit 3"])
        }
    }

    @Test("a request line reaches the child's stdin, and a line for a child that has gone is dropped")
    func sendsLines() async throws {
        try await withTemporaryDirectory { directory in
            let script = "#!/bin/bash\nread -t 20 line\necho \"got $line\"\n"
            let run = try launch(script, in: directory)
            run.child.send(#"{"type":"scan","id":"s1"}"#)
            #expect(await run.lines.wait(5) { !$0.isEmpty } == [#"got {"type":"scan","id":"s1"}"#])
            try #require(await run.exits.wait(5) { !$0.isEmpty } != nil)

            run.child.send("after the end")
            #expect(!run.child.signal(SIGTERM))
        }
    }

    @Test("a signal a parent ignores is not ignored by the child")
    func signalsAreReset() async throws {
        try await withTemporaryDirectory { directory in
            let previous = signal(SIGUSR1, SIG_IGN)
            defer { signal(SIGUSR1, previous) }
            let run = try launch("#!/bin/sh\necho ready\n" + Children.loop, in: directory)
            try #require(await run.lines.wait(5) { $0.contains("ready") } != nil)

            run.child.signal(SIGUSR1)
            let exits = try #require(await run.exits.wait(5) { !$0.isEmpty })
            #expect(exits[0].signal == SIGUSR1)
        }
    }
}

@Suite("Engine supervisor")
struct EngineSupervisorTests {
    @Test("an exit nobody asked for is reported with the end of the log, and the engine is started again")
    func restartsAfterAnExit() async throws {
        try await withTemporaryDirectory { directory in
            let script = "#!/bin/sh\necho 'panic: the reason' >&2\nexit 2\n"
            let path = try writeScript(script, named: "engine", in: directory)
            let log = directory.appendingPathComponent("logs/engine.log")
            let starts = Inbox<pid_t>()
            let downs = Inbox<EngineSupervisor.Down>()
            let supervisor = EngineSupervisor(
                launch: .init(executable: path, arguments: ["engine"], environment: toolPath, log: log),
                queue: DispatchQueue(label: "supervisor-test"),
                onStart: { starts.add($0) }, onLine: { _ in }, onDown: { downs.add($0) })
            supervisor.start()

            let first = try #require(await downs.wait(5) { !$0.isEmpty })[0]
            #expect(first.reason == "the engine exited with status 2")
            #expect(first.logTail == "panic: the reason")
            #expect(first.message == "the engine exited with status 2: panic: the reason")
            #expect(first.retryIn == 1)

            let second = try #require(await downs.wait(5) { $0.count == 2 })[1]
            #expect(second.retryIn == 2)
            // Only what the second run wrote, not the log's whole end.
            #expect(second.logTail == "panic: the reason")
            #expect(starts.all.count == 2)

            _ = await offThread { supervisor.stop(executeInFlight: false, timings: quick) }
            try await Task.sleep(for: .milliseconds(2500))
            #expect(starts.all.count == 2)
        }
    }

    @Test("an engine that cannot be started is reported and tried again")
    func missingEngine() async throws {
        try await withTemporaryDirectory { directory in
            let downs = Inbox<EngineSupervisor.Down>()
            let supervisor = EngineSupervisor(
                launch: .init(
                    executable: directory.appendingPathComponent("absent").path, arguments: [],
                    environment: [:], log: directory.appendingPathComponent("engine.log")),
                queue: DispatchQueue(label: "supervisor-test"),
                onStart: { _ in }, onLine: { _ in }, onDown: { downs.add($0) })
            supervisor.start()

            let down = try #require(await downs.wait(5) { !$0.isEmpty })[0]
            #expect(down.reason.contains("could not start"))
            #expect(down.reason.contains("No such file"))
            #expect(supervisor.stop(executeInFlight: false) == nil)
        }
    }

    @Test("stop runs the ladder on the engine and starts no other")
    func stopIsNotAnExitToReport() async throws {
        try await withTemporaryDirectory { directory in
            let path = try writeScript(Children.exitsOnStdinEnd, named: "engine", in: directory)
            let lines = Inbox<String>()
            let downs = Inbox<EngineSupervisor.Down>()
            let supervisor = EngineSupervisor(
                launch: .init(
                    executable: path, arguments: [], environment: toolPath,
                    log: directory.appendingPathComponent("engine.log")),
                queue: DispatchQueue(label: "supervisor-test"),
                onStart: { _ in },
                onLine: { lines.add(String(decoding: $0, as: UTF8.self)) },
                onDown: { downs.add($0) })
            supervisor.start()
            try #require(await lines.wait(5) { $0.contains("ready") } != nil)
            #expect(supervisor.pid != nil)

            let rung = await offThread { supervisor.stop(executeInFlight: false, timings: quick) }
            #expect(rung == .stdinClosed)
            try await Task.sleep(for: .milliseconds(300))
            #expect(downs.all.isEmpty)
            #expect(supervisor.pid == nil)
        }
    }
}

@Suite("Pid file")
struct PidFileTests {
    @Test("the app's pid and the engine's are written as two lines and read back")
    func writtenAndReadBack() async throws {
        try await withTemporaryDirectory { directory in
            let state = directory.appendingPathComponent("state/regrow")
            try PidFile(app: 4321, engine: 4322).write(to: state)

            let text = try String(contentsOf: state.appendingPathComponent("app.pid"), encoding: .utf8)
            #expect(text == "4321\n4322\n")
            #expect(PidFile.read(from: state) == PidFile(app: 4321, engine: 4322))

            PidFile.remove(from: state)
            #expect(PidFile.read(from: state) == nil)
        }
    }

    @Test("a pid file that is not two pids reads as none")
    func malformed() async throws {
        try await withTemporaryDirectory { directory in
            for text in ["", "4321\n", "4321\nengine\n", "0\n4322\n", "1\n2\n3\n"] {
                try Data(text.utf8).write(to: directory.appendingPathComponent("app.pid"))
                #expect(PidFile.read(from: directory) == nil)
            }
        }
    }

    @Test("the state directory is the one the engine's environment names")
    func stateDirectory() {
        #expect(
            PidFile.stateDirectory(environment: ["XDG_STATE_HOME": "/Users/dev/.state", "HOME": "/Users/dev"]).path
                == "/Users/dev/.state/regrow")
        #expect(
            PidFile.stateDirectory(environment: ["XDG_STATE_HOME": "", "HOME": "/Users/dev"]).path
                == "/Users/dev/.local/state/regrow")
    }

    @Test("a stale pid that now names another executable is left alone")
    func otherExecutableLeftAlone() async throws {
        let exits = Inbox<ChildProcess.Exit>()
        let sleeper = try ChildProcess.start(
            "/bin/sleep", arguments: ["20"], environment: [:], errorLog: nil,
            queue: DispatchQueue(label: "pid-test"), onLine: { _ in }, onExit: { exits.add($0) })
        defer { sleeper.signal(SIGKILL) }

        #expect(EarlierProcess.executable(of: sleeper.pid) == "/bin/sleep")
        let outcome = await offThread {
            EarlierProcess.stop(sleeper.pid, executable: "/usr/bin/true", timings: quick)
        }
        #expect(outcome == .notOurs)
        try await Task.sleep(for: .milliseconds(200))
        #expect(sleeper.isRunning)
    }

    @Test("a pid that still names the expected executable is stopped from its first SIGTERM")
    func expectedExecutableStopped() async throws {
        let exits = Inbox<ChildProcess.Exit>()
        let sleeper = try ChildProcess.start(
            "/bin/sleep", arguments: ["20"], environment: [:], errorLog: nil,
            queue: DispatchQueue(label: "pid-test"), onLine: { _ in }, onExit: { exits.add($0) })
        defer { sleeper.signal(SIGKILL) }

        let outcome = await offThread {
            EarlierProcess.stop(sleeper.pid, executable: "/bin/sleep", timings: quick)
        }
        #expect(outcome == .stopped(.terminated))
        let exit = try #require(await exits.wait(5) { !$0.isEmpty })[0]
        #expect(exit.signal == SIGTERM)
    }

    @Test("an earlier engine is given an execute's wait before the second SIGTERM, so its action ends")
    func earlierEngineFinishesItsAction() async throws {
        try await withTemporaryDirectory { directory in
            let run = try launch(Children.finishesItsAction, in: directory)
            try #require(await run.lines.wait(5) { $0.contains("ready") } != nil)
            defer { run.child.signal(SIGKILL) }

            let pid = run.child.pid
            let outcome = await offThread {
                EarlierProcess.stop(pid, executable: "/bin/bash", timings: StopTimings(stdin: 5, term: 1, secondTerm: 1))
            }
            #expect(outcome == .stopped(.terminated))
            let exit = try #require(await run.exits.wait(5) { !$0.isEmpty })[0]
            #expect(exit.status == 0)
        }
    }

    @Test("a pid no process has is not ours")
    func noSuchProcess() async throws {
        let exits = Inbox<ChildProcess.Exit>()
        let gone = try ChildProcess.start(
            "/usr/bin/true", arguments: [], environment: [:], errorLog: nil,
            queue: DispatchQueue(label: "pid-test"), onLine: { _ in }, onExit: { exits.add($0) })
        try #require(await exits.wait(5) { !$0.isEmpty } != nil)
        #expect(EarlierProcess.stop(gone.pid, executable: "/usr/bin/true", timings: quick) == .notOurs)
    }
}

@Suite("Instance lock")
struct InstanceLockTests {
    @Test("a second copy on the same state directory finds the lock taken until the first lets it go")
    func oneCopyPerStateDirectory() async throws {
        try await withTemporaryDirectory { directory in
            let state = directory.appendingPathComponent("state/regrow")
            var first = try InstanceLock.acquire(in: state)
            #expect(first != nil)
            #expect(try InstanceLock.acquire(in: state) == nil)
            #expect(try InstanceLock.acquire(in: directory.appendingPathComponent("other/regrow")) != nil)

            first = nil
            #expect(try InstanceLock.acquire(in: state) != nil)
        }
    }
}
