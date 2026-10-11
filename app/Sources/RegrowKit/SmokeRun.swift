import Foundation

/// One smoke run: the engine driven through a fixed sequence of stages,
/// with what happened left in `<dir>/result.json` and `<dir>/result.txt`.
///
/// `result.txt` is for a script: one `PASS <stage>: <detail>` or
/// `FAIL <stage>: <detail>` line per stage in order, then `RESULT ok`
/// or `RESULT failed`. The first stage that fails ends the run.
///
/// The engine's state is `<dir>/state/regrow/` in both sequences.
public final class SmokeRun {
    /// What only the app can do, each a call that blocks for its answer.
    public struct Hooks {
        /// The environment the app starts its engine with, and why the
        /// login shell's could not be read when it could not.
        public var environment: () -> (variables: [String: String], failure: String?)
        /// Whether regrow may control Finder; the owner may be asked.
        public var askFinder: () -> Bool
        /// Whether a notification would be shown, and why not.
        public var notificationsAllowed: () -> (allowed: Bool, status: String)
        /// Whether the notification centre took the notice.
        public var post: (Notice) -> Bool

        public init(
            environment: @escaping () -> (variables: [String: String], failure: String?),
            askFinder: @escaping () -> Bool,
            notificationsAllowed: @escaping () -> (allowed: Bool, status: String),
            post: @escaping (Notice) -> Bool
        ) {
            self.environment = environment
            self.askFinder = askFinder
            self.notificationsAllowed = notificationsAllowed
            self.post = post
        }
    }

    /// How long each answer is waited for.
    public struct Limits {
        public var hello: TimeInterval = 15
        public var scan: TimeInterval = 300
        public var plan: TimeInterval = 60
        public var execute: TimeInterval = 300
        public var undo: TimeInterval = 120
        public var tick: TimeInterval = 120
        public var stop = StopTimings.standard

        public init() {}
    }

    private struct Ended: Error {}

    private enum Item {
        case line(Data)
        case exit(ChildProcess.Exit)
    }

    private let directory: URL
    private let enginePath: String
    private let hooks: Hooks
    private let limits: Limits
    private let log: LogFile
    private let inbox = Mailbox<Item>()

    private var engine: ChildProcess?
    private var executing = false
    private var lines: [String] = []
    private var stages: [[String: Any]] = []
    private var result: [String: Any] = [:]

    public init(directory: URL, engine: String, hooks: Hooks, limits: Limits = Limits()) {
        self.directory = directory
        self.enginePath = engine
        self.hooks = hooks
        self.limits = limits
        log = LogFile(url: directory.appendingPathComponent("smoke.log"))
    }

    /// hello, automation, scan, plan, execute, undo: one item goes to
    /// the Trash through Finder and comes back. The rules are
    /// `<dir>/rules` and the selection is `<dir>/select.json`. True
    /// when every stage passed and both files were written.
    public func trash() -> Bool {
        run {
            let environment = engineEnvironment()
            try startEngine(["engine", "--rules-dir", directory.appendingPathComponent("rules").path], environment)
            try hello()

            let allowed = hooks.askFinder()
            result["automation_allowed"] = allowed
            guard allowed else { try fail("automation", "denied") }
            pass("automation", "allowed")

            send(["type": "scan", "id": "shell-scan"])
            let scanned = try answer(to: "shell-scan", stage: "scan", within: limits.scan)
            guard let scanID = named("start", in: scanned)?["scan_id"] as? String,
                scanned.last?["canceled"] as? Bool == false
            else { try fail("scan", "the scan did not run to its end") }
            let findings = scanned.filter { $0["event"] as? String == "finding" }.count
            pass("scan", "\(findings) \(findings == 1 ? "finding" : "findings")")

            let selectFile = directory.appendingPathComponent("select.json")
            guard let data = try? Data(contentsOf: selectFile),
                let select = try? JSONSerialization.jsonObject(with: data) as? [String]
            else { try fail("plan", "\(selectFile.path) is not a JSON list of atoms") }
            send(["type": "plan", "id": "shell-plan", "scan_id": scanID, "select": select])
            let planned = try answer(to: "shell-plan", stage: "plan", within: limits.plan)
            guard let plan = named("plan", in: planned), let planID = plan["plan_id"] as? String else {
                try fail("plan", "no plan came back")
            }
            result["plan"] = plan
            let body = plan["plan"] as? [String: Any]
            let actions = (body?["actions"] as? [[String: Any]])?.count ?? 0
            let skipped = body?["skipped"] as? [[String: Any]] ?? []
            guard actions > 0 else {
                try fail("plan", "no action planned: \(skipped.first?["reason"] as? String ?? "nothing was selected")")
            }
            pass("plan", "\(actions) \(actions == 1 ? "action" : "actions"), \(skipped.count) skipped")

            executing = true
            send(["type": "execute", "id": "shell-execute", "plan_id": planID])
            let executed = try answer(to: "shell-execute", stage: "execute", within: limits.execute, failOnError: false)
            executing = false
            let journal = executed.compactMap { $0["entry"] as? [String: Any] }
            result["journal"] = journal
            let outcome = executed.last ?? [:]
            let run = outcome["result"] as? [String: Any]
            result["result"] = run ?? NSNull()
            if outcome["event"] as? String == "error" {
                try fail("execute", Self.words(ofError: outcome))
            }
            let methods = journal.compactMap { ($0["receipt"] as? [String: Any])?["method"] as? String }
            guard let run, run["failed"] as? Int == 0, !methods.isEmpty else {
                try fail("execute", (run?["failures"] as? [String])?.first ?? "no item was moved")
            }
            // A move that fell back to staging is not in the Trash.
            if let other = methods.first(where: { $0 != "finder" }) {
                try fail("execute", "method=\(other)")
            }
            pass("execute", "method=finder")

            try undo(run["run_id"] as? String ?? planID, environment)
        }
    }

    /// hello, tick, permission, posted: one tick, and each notice its
    /// alerts give handed to the notification centre.
    public func alert(engineArguments: [String] = ["engine"]) -> Bool {
        run {
            try startEngine(engineArguments, engineEnvironment())
            try hello()

            send(["type": "tick", "id": "shell-tick", "autotrim": false])
            let ticked = try answer(to: "shell-tick", stage: "tick", within: limits.tick, failOnError: false)
            let event = named("tick", in: ticked)
            result["tick"] = event ?? NSNull()
            if let outcome = ticked.last, outcome["event"] as? String == "error" {
                try fail("tick", Self.words(ofError: outcome))
            }
            guard let event, let data = try? JSONSerialization.data(withJSONObject: event),
                let tick = Wire.decode(Wire.Tick.self, from: data)
            else { try fail("tick", "no tick event came back") }
            let alerts = tick.alerts?.count ?? 0
            pass("tick", "\(alerts) \(alerts == 1 ? "alert" : "alerts")")

            let (allowed, status) = hooks.notificationsAllowed()
            result["notifications_allowed"] = allowed
            guard allowed else { try fail("permission", "not allowed: \(status)") }
            pass("permission", "allowed")

            let notices = AlertText.notices(for: tick)
            var posted: [[String: Any]] = []
            for notice in notices {
                posted.append(["title": notice.title, "body": notice.body, "taken": hooks.post(notice)])
            }
            result["posted"] = posted
            let taken = posted.filter { $0["taken"] as? Bool == true }.count
            guard taken > 0, taken == notices.count else {
                try fail("posted", "\(taken) of \(notices.count)")
            }
            pass("posted", "\(taken) of \(notices.count)")
        }
    }

    // MARK: Stages

    private func run(_ sequence: () throws -> Void) -> Bool {
        do {
            try sequence()
        } catch is Ended {
        } catch {
            record("FAIL", "run", "\(error)")
        }
        return finish()
    }

    private func pass(_ stage: String, _ detail: String) {
        record("PASS", stage, detail)
    }

    private func fail(_ stage: String, _ detail: String) throws -> Never {
        record("FAIL", stage, detail)
        throw Ended()
    }

    private func record(_ verdict: String, _ stage: String, _ detail: String) {
        let oneLine = detail.split(whereSeparator: \.isNewline).joined(separator: " ")
        lines.append("\(verdict) \(stage): \(oneLine)")
        stages.append(["stage": stage, "ok": verdict == "PASS", "detail": detail])
        log("\(verdict) \(stage): \(oneLine)")
    }

    /// Stops the engine and writes both files. `result.txt` is written
    /// last and in one step, so a reader never sees half of it.
    private func finish() -> Bool {
        if let engine {
            let rung = engine.stop(executeInFlight: executing, timings: limits.stop)
            log("engine stopped by \(rung.rawValue)")
        }
        let failed = stages.filter { $0["ok"] as? Bool == false }.compactMap { $0["stage"] as? String }
        let ok = failed.isEmpty
        result["ok"] = ok
        result["failed"] = failed
        result["stages"] = stages
        do {
            let json = try JSONSerialization.data(withJSONObject: result, options: [.prettyPrinted, .sortedKeys])
            try json.write(to: directory.appendingPathComponent("result.json"), options: .atomic)
            let text = (lines + ["RESULT \(ok ? "ok" : "failed")"]).joined(separator: "\n") + "\n"
            try Data(text.utf8).write(to: directory.appendingPathComponent("result.txt"), options: .atomic)
        } catch {
            log("could not write the result: \(error)")
            return false
        }
        return ok
    }

    private func undo(_ runID: String, _ environment: [String: String]) throws {
        let errors = directory.appendingPathComponent("undo-stderr.log")
        try? FileManager.default.removeItem(at: errors)
        let printed = Mailbox<Item>()
        let child: ChildProcess
        do {
            child = try ChildProcess.start(
                enginePath, arguments: ["undo", runID], environment: environment, errorLog: errors,
                queue: DispatchQueue(label: "regrow.smoke.undo"),
                onLine: { printed.put(.line($0)) }, onExit: { printed.put(.exit($0)) })
        } catch {
            try fail("undo", "\(error)")
        }
        var output: [String] = []
        var status: Int32?
        let deadline = Date().addingTimeInterval(limits.undo)
        reading: while let item = printed.take(until: deadline) {
            switch item {
            case .line(let line):
                output.append(String(decoding: line, as: UTF8.self))
            case .exit(let exit):
                status = exit.status ?? -1
                break reading
            }
        }
        if status == nil {
            _ = child.stop(executeInFlight: false, timings: limits.stop)
        }
        if let text = try? String(contentsOf: errors, encoding: .utf8) {
            output += text.split(whereSeparator: \.isNewline).map(String.init)
        }
        result["undo"] = ["status": status.map { Int($0) as Any } ?? NSNull(), "output": output.joined(separator: "\n")]
        let last = output.last { !$0.trimmingCharacters(in: .whitespaces).isEmpty }
        guard let status else { try fail("undo", "did not finish within \(Int(limits.undo)) seconds") }
        guard status == 0 else { try fail("undo", last ?? "exit status \(status)") }
        pass("undo", last ?? "exit status 0")
    }

    // MARK: The engine

    private func engineEnvironment() -> [String: String] {
        let read = hooks.environment()
        if let failure = read.failure {
            result["environment"] = ["captured": false, "reason": failure]
        } else {
            result["environment"] = ["captured": true]
        }
        var environment = read.variables
        environment["XDG_STATE_HOME"] = directory.appendingPathComponent("state").path
        return environment
    }

    private func startEngine(_ arguments: [String], _ environment: [String: String]) throws {
        do {
            let inbox = self.inbox
            let engine = try ChildProcess.start(
                enginePath, arguments: arguments, environment: environment,
                errorLog: directory.appendingPathComponent("engine.log"),
                queue: DispatchQueue(label: "regrow.smoke.engine"),
                onLine: { inbox.put(.line($0)) }, onExit: { inbox.put(.exit($0)) })
            self.engine = engine
            log("engine started: \(enginePath) \(arguments.joined(separator: " ")), pid \(engine.pid)")
        } catch {
            try fail("hello", "\(error)")
        }
    }

    private func hello() throws {
        guard let event = try next(stage: "hello", until: Date().addingTimeInterval(limits.hello)),
            event["event"] as? String == "hello", let fda = event["fda"] as? String
        else { try fail("hello", "the engine's first line was not a hello") }
        result["fda"] = fda
        pass("hello", "fda=\(fda)")
    }

    private func send(_ request: [String: Any]) {
        guard let line = Wire.line(request) else { return }
        log("→ \(line)")
        engine?.send(line)
    }

    /// Every event that answers `id`, its terminal event last.
    private func answer(
        to id: String, stage: String, within seconds: TimeInterval, failOnError: Bool = true
    ) throws -> [[String: Any]] {
        let deadline = Date().addingTimeInterval(seconds)
        var events: [[String: Any]] = []
        while let event = try next(stage: stage, until: deadline) {
            guard event["re"] as? String == id else { continue }
            events.append(event)
            let name = event["event"] as? String
            if name == "error", failOnError {
                try fail(stage, Self.words(ofError: event))
            }
            if name == "done" || name == "error" {
                log("← \(name ?? "") re=\(id)")
                return events
            }
        }
        try fail(stage, "no answer within \(Int(seconds)) seconds")
    }

    private func next(stage: String, until deadline: Date) throws -> [String: Any]? {
        while let item = inbox.take(until: deadline) {
            switch item {
            case .line(let line):
                if let event = (try? JSONSerialization.jsonObject(with: line)) as? [String: Any] {
                    return event
                }
            case .exit(let exit):
                engine = nil
                try fail(stage, "the engine \(exit)")
            }
        }
        return nil
    }

    private func named(_ event: String, in events: [[String: Any]]) -> [String: Any]? {
        events.first { $0["event"] as? String == event }
    }

    private static func words(ofError event: [String: Any]) -> String {
        "\(event["code"] as? String ?? "error"): \(event["message"] as? String ?? "")"
    }
}

/// Values handed from one thread to another that waits for them, in
/// order.
public final class Mailbox<Value>: @unchecked Sendable {
    private let condition = NSCondition()
    private var values: [Value] = []

    public init() {}

    public func put(_ value: Value) {
        condition.lock()
        values.append(value)
        condition.signal()
        condition.unlock()
    }

    /// The next value, nil when none came by `deadline`.
    public func take(until deadline: Date) -> Value? {
        condition.lock()
        defer { condition.unlock() }
        while values.isEmpty {
            if !condition.wait(until: deadline) {
                return nil
            }
        }
        return values.removeFirst()
    }
}
