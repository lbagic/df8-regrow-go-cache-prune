import Foundation
import RegrowKit
import Testing

@Suite("Login environment")
struct LoginEnvironmentTests {
    private let opening = "<<begin>>"
    private let closing = "<<end>>"

    private func output(_ entries: [String], before: String = "", after: String = "") -> Data {
        var data = Data(before.utf8)
        data.append(Data(opening.utf8))
        for entry in entries {
            data.append(Data(entry.utf8))
            data.append(0)
        }
        data.append(Data(closing.utf8))
        data.append(Data(after.utf8))
        return data
    }

    @Test("only what lies between the markers is read, whatever a profile printed around them")
    func textAroundTheMarkers() throws {
        let printed = output(
            ["PATH=/opt/tools/bin:/usr/bin", "HOME=/Users/dev"],
            before: "Last login: today\nNOT=a variable\u{0}", after: "\nbye\nALSO=not one\u{0}")
        let environment = try LoginEnvironment.parse(printed, opening: opening, closing: closing).get()
        #expect(environment == ["PATH": "/opt/tools/bin:/usr/bin", "HOME": "/Users/dev"])
    }

    @Test("a value keeps its newlines and its = signs")
    func newlinesAndEquals() throws {
        let printed = output(["PROMPT=line one\nline two", "FLAGS=-ldflags=-X main.v=1", "PATH=/usr/bin"])
        let environment = try LoginEnvironment.parse(printed, opening: opening, closing: closing).get()
        #expect(environment["PROMPT"] == "line one\nline two")
        #expect(environment["FLAGS"] == "-ldflags=-X main.v=1")
        #expect(environment["PATH"] == "/usr/bin")
    }

    @Test("an empty value is a variable that is set and empty")
    func emptyValue() throws {
        let printed = output(["EMPTY=", "PATH=/usr/bin", "no equals sign", "=no name"])
        let environment = try LoginEnvironment.parse(printed, opening: opening, closing: closing).get()
        #expect(environment == ["EMPTY": "", "PATH": "/usr/bin"])
    }

    @Test("output with no closing marker is refused: the shell was cut short")
    func noClosingMarker() {
        var printed = Data(opening.utf8)
        printed.append(Data("PATH=/usr/bin\u{0}HOME=/Users/d".utf8))
        let result = LoginEnvironment.parse(printed, opening: opening, closing: closing)
        #expect(result == .failure(.init(reason: "the shell's output ended before its environment did")))
    }

    @Test("output with no opening marker is refused")
    func noOpeningMarker() {
        let result = LoginEnvironment.parse(Data("PATH=/usr/bin".utf8), opening: opening, closing: closing)
        #expect(result == .failure(.init(reason: "the shell's output has no environment in it")))
    }

    @Test("the shell is run as a login, interactive shell and its environment is read from between its own noise")
    func capturesFromAShell() async throws {
        try await withTemporaryDirectory { directory in
            // A stand-in for a login shell: it checks its flags, sets a
            // variable as a profile would, and prints around the command.
            let shell = try writeScript(
                """
                #!/bin/sh
                [ "$1" = -l ] && [ "$2" = -i ] && [ "$3" = -c ] || exit 9
                echo "profile says hello"
                REGROW_TEST_VALUE='a=b
                c'
                export REGROW_TEST_VALUE
                eval "$4"
                echo "profile says bye"
                """, named: "shell", in: directory)

            let environment = try await offThread { LoginEnvironment.capture(shell: shell, timeout: 5) }.get()
            #expect(environment["REGROW_TEST_VALUE"] == "a=b\nc")
        }
    }

    @Test("zsh and bash give up their environment as login, interactive shells with no terminal", arguments: [
        "/bin/zsh", "/bin/bash",
    ])
    func systemShells(binary: String) async throws {
        try await withTemporaryDirectory { directory in
            // The system's own shell with an empty home in the temp
            // dir, so no profile of the user's is read.
            let home = directory.appendingPathComponent("home")
            try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
            let shell = try writeScript(
                """
                #!/bin/sh
                exec /usr/bin/env -i HOME="\(home.path)" ZDOTDIR="\(home.path)" REGROW_TEST_SHELL=1 \(binary) "$@"
                """, named: "shell", in: directory)

            let environment = try await offThread { LoginEnvironment.capture(shell: shell, timeout: 10) }.get()
            #expect(environment["HOME"] == home.path)
            #expect(environment["REGROW_TEST_SHELL"] == "1")
            #expect(environment["PATH"]?.contains("/usr/bin") == true)
        }
    }

    @Test("a shell that never finishes is killed with its children after the timeout, and the failure says why")
    func timesOut() async throws {
        try await withTemporaryDirectory { directory in
            let shell = try writeScript(
                """
                #!/bin/sh
                [ "$1" = -l ] || exit 0
                sleep 20 &
                echo $! > "$(dirname "$0")/child.pid"
                wait
                """, named: "shell", in: directory)
            // The first run of a new file can take macOS a second to
            // allow, which would use up the timeout under test.
            let warmed = Inbox<ChildProcess.Exit>()
            _ = try ChildProcess.start(
                shell, arguments: [], environment: toolPath, errorLog: nil,
                queue: DispatchQueue(label: "warm"), onLine: { _ in }, onExit: { warmed.add($0) })
            try #require(await warmed.wait(10) { !$0.isEmpty } != nil)

            let started = Date()
            let result = await offThread { LoginEnvironment.capture(shell: shell, timeout: 2) }
            #expect(result == .failure(.init(reason: "\(shell) did not print its environment within 2 seconds")))
            #expect(Date().timeIntervalSince(started) < 5)

            let text = try String(contentsOf: directory.appendingPathComponent("child.pid"), encoding: .utf8)
            let child = try #require(pid_t(text.trimmingCharacters(in: .whitespacesAndNewlines)))
            let gone = await Inbox<Int>().wait(3) { _ in kill(child, 0) == -1 && errno == ESRCH }
            #expect(gone != nil)
        }
    }

    @Test("a shell that ends without printing its environment is a failure with its status")
    func shellFails() async throws {
        try await withTemporaryDirectory { directory in
            let shell = try writeScript("#!/bin/sh\nexit 3\n", named: "shell", in: directory)
            let result = await offThread { LoginEnvironment.capture(shell: shell, timeout: 5) }
            #expect(result == .failure(.init(reason: "\(shell) ended with status 3 before it printed its environment")))
        }
    }

    @Test("a shell that cannot be started is a failure that names it")
    func shellMissing() async {
        let result = await offThread { LoginEnvironment.capture(shell: "/nonexistent/shell", timeout: 5) }
        guard case .failure(let failure) = result else {
            Issue.record("a missing shell cannot succeed")
            return
        }
        #expect(failure.reason.contains("could not start /nonexistent/shell"))
    }
}
