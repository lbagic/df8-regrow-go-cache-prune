import Foundation
import RegrowKit
import Testing

/// The smoke sequences against a stand-in engine: a script that answers
/// each request with canned events, as the protocol shapes them, and
/// notes how it was called. Nothing here reaches Finder, the Trash or
/// the notification centre.
@Suite("Smoke run")
struct SmokeRunTests {
    private static let bundledRules = URL(fileURLWithPath: "/Applications/Regrow.app/Contents/Resources/smoke")

    private func standInEngine(receiptMethod: String = "finder", in directory: URL) throws -> String {
        let script = #"""
            #!/bin/bash
            here="$(dirname "$0")"
            if [ "$1" = undo ]; then
              echo "$2 | $XDG_STATE_HOME" > "$here/undo-call.txt"
              echo "restoring run $2"
              echo "restored 1 item"
              exit 0
            fi
            echo "$* | $XDG_STATE_HOME" > "$here/engine-call.txt"
            echo '{"event":"hello","protocol":1,"version":"0.0.0-test","fda":"granted"}'
            while read -t 20 line; do
              case "$line" in
                *'"type":"scan"'*)
                  echo '{"event":"start","re":"shell-scan","scan_id":"scan-1","rules":1}'
                  echo '{"event":"finding","re":"shell-scan","scan_id":"scan-1","index":0,"finding":{"rule":{"id":"sentinel"},"items":[]}}'
                  echo '{"event":"done","re":"shell-scan","elapsed_ms":1,"canceled":false}' ;;
                *'"type":"plan"'*)
                  echo "$line" > "$here/plan-request.json"
                  echo '{"event":"plan","re":"shell-plan","plan_id":"run-1","plan":{"actions":[{"rule_id":"sentinel","kind":"trash","path":"/Users/dev/Sentinel"}]},"totals":{}}'
                  echo '{"event":"done","re":"shell-plan"}' ;;
                *'"type":"execute"'*)
                  echo '{"event":"journal","re":"shell-execute","entry":{"run":"run-1","seq":1,"event":"start","rule_id":"sentinel"}}'
                  echo '{"event":"journal","re":"shell-execute","entry":{"run":"run-1","seq":1,"event":"done","receipt":{"original":"/Users/dev/Sentinel","to":"/Users/dev/.Trash/Sentinel","method":"METHOD"}}}'
                  echo '{"event":"done","re":"shell-execute","result":{"run_id":"run-1","done":1,"failed":0,"bytes":8192},"canceled":false}' ;;
                *'"type":"tick"'*)
                  echo '{"event":"tick","re":"shell-tick","at":"2026-01-01T00:00:00Z","total":494384795648,"free":9663676416,"purgeable":0,"swap_used":0,"alerts":[{"kind":"free-fall","message":"Free space fell 6.0 GiB in the last 30 minutes.","acute":true}]}'
                  echo '{"event":"done","re":"shell-tick"}' ;;
              esac
            done

            """#
        return try writeScript(
            script.replacingOccurrences(of: "METHOD", with: receiptMethod), named: "regrow-engine", in: directory)
    }

    private final class Posted: @unchecked Sendable {
        var notices: [Notice] = []
    }

    private func smoke(
        _ directory: URL, engine: String, finderAllows: Bool = true,
        notifications: (allowed: Bool, status: String) = (true, "allowed"), posted: Posted = Posted()
    ) -> SmokeRun {
        var limits = SmokeRun.Limits()
        limits.hello = 10
        limits.stop = StopTimings(stdin: 2, term: 1, secondTerm: 1)
        return SmokeRun(
            directory: directory, engine: engine,
            hooks: .init(
                environment: { (toolPath, nil) },
                askFinder: { finderAllows },
                notificationsAllowed: { notifications },
                post: { notice in
                    posted.notices.append(notice)
                    return true
                }),
            limits: limits)
    }

    private func text(_ name: String, in directory: URL) -> String? {
        try? String(contentsOf: directory.appendingPathComponent(name), encoding: .utf8)
    }

    private func json(_ name: String, in directory: URL) throws -> [String: Any] {
        let data = try Data(contentsOf: directory.appendingPathComponent(name))
        return try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    @Test("the trash sequence passes its six stages in order and leaves both result files")
    func trashPasses() async throws {
        try await withTemporaryDirectory { directory in
            let engine = try standInEngine(in: directory)
            let run = smoke(directory, engine: engine)

            #expect(await offThread { run.trash(rules: Self.bundledRules) })

            #expect(text("result.txt", in: directory) == """
                PASS hello: fda=granted
                PASS automation: allowed
                PASS scan: 1 finding
                PASS plan: 1 action, 0 skipped
                PASS execute: method=finder
                PASS undo: restored 1 item
                RESULT ok

                """)

            let result = try json("result.json", in: directory)
            #expect(result["ok"] as? Bool == true)
            #expect(result["failed"] as? [String] == [])
            #expect(result["fda"] as? String == "granted")
            #expect(result["automation_allowed"] as? Bool == true)
            #expect((result["plan"] as? [String: Any])?["plan_id"] as? String == "run-1")
            #expect((result["journal"] as? [[String: Any]])?.count == 2)
            #expect((result["result"] as? [String: Any])?["run_id"] as? String == "run-1")
            let undo = try #require(result["undo"] as? [String: Any])
            #expect(undo["status"] as? Int == 0)
            #expect(undo["output"] as? String == "restoring run run-1\nrestored 1 item")
            #expect((result["environment"] as? [String: Any])?["captured"] as? Bool == true)

            // The state directory is the smoke directory's own, for the
            // engine and for undo alike; the rules are the bundle's.
            let state = directory.appendingPathComponent("state").path
            #expect(text("engine-call.txt", in: directory) == "engine --rules-dir \(Self.bundledRules.path) | \(state)\n")
            #expect(text("undo-call.txt", in: directory) == "run-1 | \(state)\n")
            let planned = try json("plan-request.json", in: directory)
            #expect(planned["select"] as? [String] == ["smoke-sentinel"])
            #expect(planned["scan_id"] as? String == "scan-1")
        }
    }

    @Test("a denied Automation ends the trash sequence there, before anything is scanned")
    func automationDenied() async throws {
        try await withTemporaryDirectory { directory in
            let engine = try standInEngine(in: directory)
            let run = smoke(directory, engine: engine, finderAllows: false)

            #expect(await offThread { run.trash(rules: Self.bundledRules) } == false)

            #expect(text("result.txt", in: directory) == """
                PASS hello: fda=granted
                FAIL automation: denied
                RESULT failed

                """)
            let result = try json("result.json", in: directory)
            #expect(result["ok"] as? Bool == false)
            #expect(result["failed"] as? [String] == ["automation"])
            #expect(text("plan-request.json", in: directory) == nil)
        }
    }

    @Test("a move that fell back to staging fails the execute stage, and the journal is still in the result")
    func stagingFails() async throws {
        try await withTemporaryDirectory { directory in
            let engine = try standInEngine(receiptMethod: "staging", in: directory)
            let run = smoke(directory, engine: engine)

            #expect(await offThread { run.trash(rules: Self.bundledRules) } == false)

            let lines = text("result.txt", in: directory)?.split(separator: "\n").map(String.init) ?? []
            #expect(lines.suffix(2) == ["FAIL execute: method=staging", "RESULT failed"])
            let result = try json("result.json", in: directory)
            #expect((result["journal"] as? [[String: Any]])?.count == 2)
            #expect(text("undo-call.txt", in: directory) == nil)
        }
    }

    @Test("rules and a selection the smoke directory holds are not read: the bundle's rule is planned alone")
    func callerRulesIgnored() async throws {
        try await withTemporaryDirectory { directory in
            let engine = try standInEngine(in: directory)
            let callers = directory.appendingPathComponent("rules")
            try FileManager.default.createDirectory(at: callers, withIntermediateDirectories: true)
            try Data("id: messages\n".utf8).write(to: callers.appendingPathComponent("messages.yaml"))
            try Data(#"["messages"]"#.utf8).write(to: directory.appendingPathComponent("select.json"))
            let run = smoke(directory, engine: engine)

            #expect(await offThread { run.trash(rules: Self.bundledRules) })

            let call = text("engine-call.txt", in: directory) ?? ""
            #expect(call.hasPrefix("engine --rules-dir \(Self.bundledRules.path) |"))
            #expect(try json("plan-request.json", in: directory)["select"] as? [String] == ["smoke-sentinel"])
        }
    }

    @Test("the alert sequence posts what the tick's alerts give and passes its four stages")
    func alertPasses() async throws {
        try await withTemporaryDirectory { directory in
            let engine = try standInEngine(in: directory)
            let posted = Posted()
            let run = smoke(directory, engine: engine, posted: posted)

            #expect(await offThread { run.alert() })

            #expect(text("result.txt", in: directory) == """
                PASS hello: fda=granted
                PASS tick: 1 alert
                PASS permission: allowed
                PASS posted: 1 of 1
                RESULT ok

                """)
            #expect(posted.notices == [
                Notice(title: "Free space is falling fast", body: "Free space fell 6.0 GiB in the last 30 minutes.")
            ])
            let result = try json("result.json", in: directory)
            #expect(result["notifications_allowed"] as? Bool == true)
            #expect((result["tick"] as? [String: Any])?["free"] as? Int == 9_663_676_416)
            let notices = try #require(result["posted"] as? [[String: Any]])
            #expect(notices.first?["title"] as? String == "Free space is falling fast")
            #expect(notices.first?["taken"] as? Bool == true)
            let state = directory.appendingPathComponent("state").path
            #expect(text("engine-call.txt", in: directory) == "engine | \(state)\n")
        }
    }

    @Test("notifications that are not allowed fail the permission stage, and nothing is posted")
    func alertWithoutPermission() async throws {
        try await withTemporaryDirectory { directory in
            let engine = try standInEngine(in: directory)
            let posted = Posted()
            let run = smoke(
                directory, engine: engine, notifications: (false, "denied in System Settings"), posted: posted)

            #expect(await offThread { run.alert() } == false)

            #expect(text("result.txt", in: directory) == """
                PASS hello: fda=granted
                PASS tick: 1 alert
                FAIL permission: not allowed: denied in System Settings
                RESULT failed

                """)
            #expect(posted.notices.isEmpty)
        }
    }

    @Test("an engine that cannot be started fails the hello stage")
    func noEngine() async throws {
        try await withTemporaryDirectory { directory in
            let run = smoke(directory, engine: directory.appendingPathComponent("absent").path)

            #expect(await offThread { run.alert() } == false)

            let lines = text("result.txt", in: directory)?.split(separator: "\n").map(String.init) ?? []
            #expect(lines.count == 2)
            #expect(lines.first?.hasPrefix("FAIL hello: could not start ") == true)
            #expect(lines.last == "RESULT failed")
        }
    }

    @Test("an engine that exits in the middle of a stage fails that stage with how it ended")
    func engineDies() async throws {
        try await withTemporaryDirectory { directory in
            let script = "#!/bin/bash\necho '{\"event\":\"hello\",\"protocol\":1,\"version\":\"t\",\"fda\":\"denied\"}'\nread -t 20 line\nexit 4\n"
            let engine = try writeScript(script, named: "regrow-engine", in: directory)
            let run = smoke(directory, engine: engine)

            #expect(await offThread { run.alert() } == false)

            #expect(text("result.txt", in: directory) == """
                PASS hello: fda=denied
                FAIL tick: the engine exited with status 4
                RESULT failed

                """)
        }
    }
}
