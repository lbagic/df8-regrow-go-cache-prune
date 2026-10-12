import Foundation
import RegrowKit
import Testing

/// The engine binary of this checkout, driven as the shell drives it.
/// Its home, its state and its one rule are in a temp dir, and it is
/// never asked to execute: a Trash move would reach the real Finder.
@Suite("Real engine")
struct RealEngineTests {
    private static let rule = """
        id: sentinel
        title: Sentinel folder
        category: fixture
        risk: safe
        paths:
          darwin:
            - ~/Sentinel
        regen:
          story: A folder this test made.
          cost: none
        """

    private func buildEngine(into directory: URL) async throws -> String {
        let checkout = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
        let binary = directory.appendingPathComponent("regrow").path
        let build = Process()
        build.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        build.arguments = ["go", "build", "-trimpath", "-o", binary, "./cmd/regrow"]
        build.currentDirectoryURL = checkout
        let output = Pipe()
        build.standardOutput = output
        build.standardError = output
        try build.run()
        let printed = await offThread { output.fileHandleForReading.readDataToEndOfFile() }
        build.waitUntilExit()
        try #require(
            build.terminationStatus == 0,
            "go build failed: \(String(decoding: printed, as: UTF8.self))")
        return binary
    }

    /// The router and the engine wired as the app wires them.
    private final class Session: @unchecked Sendable {
        let queue = DispatchQueue(label: "engine-test")
        let shell = Inbox<[String: Any]>()
        let page = Inbox<[String: Any]>()
        let exits = Inbox<ChildProcess.Exit>()
        private var router = Router()
        private var child: ChildProcess?

        func start(_ engine: String, arguments: [String], environment: [String: String], log: URL) throws {
            child = try ChildProcess.start(
                engine, arguments: arguments, environment: environment, errorLog: log, queue: queue,
                onLine: { [self] line in apply(router.engineLine(line)) },
                onExit: { [self] exit in exits.add(exit) })
        }

        func fromPage(_ fields: [String: Any]) {
            queue.sync { apply(router.pageMessage(fields)) }
        }

        func fromShell(_ fields: [String: Any]) {
            queue.sync { apply(router.shellRequest(fields)) }
        }

        func stop() async -> StopRung? {
            guard let child else { return nil }
            return await offThread { child.stop(executeInFlight: false) }
        }

        private func apply(_ outputs: [Router.Output]) {
            for output in outputs {
                switch output {
                case .engine(let line): child?.send(line)
                case .page(let line): page.add(Self.object(line))
                case .shell(let line): shell.add(Self.object(line))
                case .command: break
                }
            }
        }

        private static func object(_ line: String) -> [String: Any] {
            (try? JSONSerialization.jsonObject(with: Data(line.utf8))) as? [String: Any] ?? [:]
        }
    }

    private func ended(_ id: String) -> ([[String: Any]]) -> Bool {
        { events in
            events.contains {
                $0["re"] as? String == id && ($0["event"] as? String == "done" || $0["event"] as? String == "error")
            }
        }
    }

    private func events(_ all: [[String: Any]], _ name: String, re: String) -> [[String: Any]] {
        all.filter { $0["event"] as? String == name && $0["re"] as? String == re }
    }

    @Test("hello arrives, a scan streams one finding and ends, a plan comes back, and a tick comes back")
    func helloScanPlanTick() async throws {
        try await withTemporaryDirectory { directory in
            let engine = try await buildEngine(into: directory)
            let home = directory.appendingPathComponent("home")
            let state = directory.appendingPathComponent("state")
            let rules = directory.appendingPathComponent("rules")
            let sentinel = home.appendingPathComponent("Sentinel")
            for folder in [sentinel, state, rules] {
                try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            }
            try Data(count: 8192).write(to: sentinel.appendingPathComponent("keep.bin"))
            try Data(Self.rule.utf8).write(to: rules.appendingPathComponent("sentinel.yaml"))

            let session = Session()
            try session.start(
                engine, arguments: ["engine", "--rules-dir", rules.path],
                environment: toolPath.merging(["HOME": home.path, "XDG_STATE_HOME": state.path]) { $1 },
                log: directory.appendingPathComponent("engine.log"))

            let hellos = try #require(await session.shell.wait(20) { !$0.isEmpty })
            #expect(hellos[0]["event"] as? String == "hello")
            #expect(hellos[0]["protocol"] as? Int == 1)
            #expect(["granted", "denied", "unknown"].contains(hellos[0]["fda"] as? String ?? ""))
            #expect(session.page.all.first?["event"] as? String == "hello")

            // The tick is sent while the scan is in flight: the router
            // holds it, so the engine never answers busy.
            session.fromPage(["type": "scan", "id": "s1"])
            session.fromShell(["type": "tick", "id": "shell-tick-1", "autotrim": false])

            let scanned = try #require(await session.page.wait(60, until: ended("s1")))
            let start = try #require(events(scanned, "start", re: "s1").first)
            let scanID = try #require(start["scan_id"] as? String)
            #expect(start["rules"] as? Int == 1)
            #expect(events(scanned, "headroom", re: "s1").count == 1)
            let findings = events(scanned, "finding", re: "s1")
            try #require(findings.count == 1)
            let finding = try #require(findings[0]["finding"] as? [String: Any])
            #expect((finding["rule"] as? [String: Any])?["id"] as? String == "sentinel")
            let items = try #require(finding["items"] as? [[String: Any]])
            #expect(items.count == 1)
            #expect((items.first?["bytes"] as? Int ?? 0) >= 8192)
            #expect(events(scanned, "summary", re: "s1").count == 1)
            let scanDone = try #require(events(scanned, "done", re: "s1").first)
            #expect(scanDone["canceled"] as? Bool == false)

            let ticked = try #require(await session.shell.wait(60, until: ended("shell-tick-1")))
            let tickLine = try #require(events(ticked, "tick", re: "shell-tick-1").first)
            let tick = try #require(
                Wire.decode(Wire.Tick.self, from: try JSONSerialization.data(withJSONObject: tickLine)))
            #expect(tick.total > 0)
            #expect(tick.free > 0 && tick.free <= tick.total)
            #expect(events(ticked, "done", re: "shell-tick-1").count == 1)
            let refused = (scanned + ticked + session.page.all + session.shell.all).filter {
                $0["code"] as? String == "busy"
            }
            #expect(refused.isEmpty)

            session.fromPage(["type": "plan", "id": "p1", "scan_id": scanID])
            let planned = try #require(await session.page.wait(30, until: ended("p1")))
            let plan = try #require(events(planned, "plan", re: "p1").first)
            #expect(!(plan["plan_id"] as? String ?? "").isEmpty)
            let actions = try #require((plan["plan"] as? [String: Any])?["actions"] as? [[String: Any]])
            try #require(actions.count == 1)
            #expect(actions[0]["kind"] as? String == "trash")
            #expect((actions[0]["path"] as? String ?? "").hasSuffix("/Sentinel"))
            #expect(events(planned, "done", re: "p1").count == 1)

            #expect(await session.stop() == .stdinClosed)
            let exit = try #require(await session.exits.wait(5) { !$0.isEmpty })[0]
            #expect(exit.status == 0)

            // Nothing was executed, and the engine kept its state where
            // it was told to.
            #expect(FileManager.default.fileExists(atPath: sentinel.appendingPathComponent("keep.bin").path))
            let kept = try FileManager.default.contentsOfDirectory(
                atPath: state.appendingPathComponent("regrow").path)
            #expect(!kept.isEmpty)
        }
    }

    @Test("the bundle's smoke rule lists only ~/regrow-smoke-* and plans its Trash move")
    func bundledSmokeRule() async throws {
        try await withTemporaryDirectory { directory in
            let engine = try await buildEngine(into: directory)
            let rules = URL(fileURLWithPath: #filePath)
                .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
                .appendingPathComponent("Resources/smoke")
            let home = directory.appendingPathComponent("home")
            let state = directory.appendingPathComponent("state")
            for folder in ["regrow-smoke-123", "regrow-smoke", "Library/Messages"] {
                let path = home.appendingPathComponent(folder)
                try FileManager.default.createDirectory(at: path, withIntermediateDirectories: true)
                try Data(count: 4096).write(to: path.appendingPathComponent("keep.bin"))
            }
            try FileManager.default.createDirectory(at: state, withIntermediateDirectories: true)

            let session = Session()
            try session.start(
                engine, arguments: ["engine", "--rules-dir", rules.path],
                environment: toolPath.merging(["HOME": home.path, "XDG_STATE_HOME": state.path]) { $1 },
                log: directory.appendingPathComponent("engine.log"))
            try #require(await session.shell.wait(20) { !$0.isEmpty } != nil)

            session.fromPage(["type": "scan", "id": "s1"])
            let scanned = try #require(await session.page.wait(60, until: ended("s1")))
            let scanID = try #require(events(scanned, "start", re: "s1").first?["scan_id"] as? String)
            let findings = events(scanned, "finding", re: "s1")
            try #require(findings.count == 1)
            let finding = try #require(findings[0]["finding"] as? [String: Any])
            #expect((finding["rule"] as? [String: Any])?["id"] as? String == SmokeRun.sentinelRule)
            let items = try #require(finding["items"] as? [[String: Any]])
            #expect(items.compactMap { $0["path"] as? String } == [home.appendingPathComponent("regrow-smoke-123").path])

            session.fromPage(["type": "plan", "id": "p1", "scan_id": scanID, "select": [SmokeRun.sentinelRule]])
            let planned = try #require(await session.page.wait(30, until: ended("p1")))
            let plan = try #require(events(planned, "plan", re: "p1").first)
            let actions = try #require((plan["plan"] as? [String: Any])?["actions"] as? [[String: Any]])
            #expect(actions.map { $0["kind"] as? String } == ["trash"])
            #expect(actions.map { $0["path"] as? String } == [home.appendingPathComponent("regrow-smoke-123").path])

            #expect(await session.stop() == .stdinClosed)
        }
    }
}
