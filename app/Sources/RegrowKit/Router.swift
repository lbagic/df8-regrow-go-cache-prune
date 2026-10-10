import Foundation

/// The shell's place between the page and the engine: it is the only
/// writer to the engine's stdin, holds a scan, execute or tick behind
/// the one in flight, and sends every engine event to its side.
///
/// It is a value with no I/O: each input returns what to do.
public struct Router {
    public enum Output: Equatable {
        /// A request line to write to the engine.
        case engine(String)
        /// An event line for the page's `window.regrow.receive`.
        case page(String)
        /// An event line for the shell itself.
        case shell(String)
        /// A `{shell: …}` message from the page, by its name.
        case command(String)
    }

    /// Ids with this prefix are the shell's; the page may not use them.
    public static let shellPrefix = "shell-"
    /// The engine discards a longer request line, unanswered by id.
    public static let maxLine = 1 << 20

    private static let pageTypes: Set<String> = ["scan", "plan", "execute", "cancel"]
    private static let slotTypes: Set<String> = ["scan", "execute", "tick"]

    private enum Origin { case page, shell }

    private struct Request {
        let id: String
        let type: String
        let line: String
        let origin: Origin
        let target: String?
        let planID: String?
    }

    private var up = false
    private var inFlight: Request?
    private var waiting: [Request] = []
    /// `plan` and `cancel` sent while no engine was up.
    private var parked: [Request] = []
    private var serial = 0

    public init() {}

    /// The stop ladder waits longer for an engine that is executing.
    public var executeInFlight: Bool { inFlight?.type == "execute" }

    /// A request of the shell's own; its `id` starts `shell-`.
    public mutating func shellRequest(_ fields: [String: Any]) -> [Output] {
        guard let type = fields["type"] as? String, let id = fields["id"] as? String,
            id.hasPrefix(Self.shellPrefix), let line = Wire.line(fields)
        else {
            preconditionFailure("a shell request needs a type and an id starting \(Self.shellPrefix)")
        }
        return submit(request(type: type, id: id, line: line, origin: .shell, fields: fields))
    }

    /// A message the page posted. A request the shell does not forward
    /// is answered here, as the engine would answer it.
    public mutating func pageMessage(_ body: Any) -> [Output] {
        guard let fields = body as? [String: Any] else {
            return [.page(Self.error(re: nil, code: "bad_request", message: "a message from the page must be an object"))]
        }
        if let command = fields["shell"] as? String {
            return [.command(command)]
        }
        let id = fields["id"] as? String
        guard let type = fields["type"] as? String, Self.pageTypes.contains(type) else {
            return [.page(Self.error(re: id, code: "unknown_request", message: "the page may send scan, plan, execute and cancel"))]
        }
        guard let id, !id.isEmpty else {
            return [.page(Self.error(re: nil, code: "bad_request", message: "a request needs a string id"))]
        }
        guard !id.hasPrefix(Self.shellPrefix) else {
            return [.page(Self.error(re: id, code: "bad_request", message: "ids starting \(Self.shellPrefix) are the shell's own"))]
        }
        // The slot is released by the id of the request in flight, so
        // no other request may carry that id meanwhile.
        guard !holds(id) else {
            return [.page(Self.error(re: id, code: "bad_request", message: "request id \(id) is still in use"))]
        }
        guard let line = Wire.line(fields) else {
            return [.page(Self.error(re: id, code: "bad_request", message: "the request is not JSON"))]
        }
        guard line.utf8.count <= Self.maxLine else {
            return [.page(Self.error(re: id, code: "line_too_long", message: "the request is over 1 MiB"))]
        }
        return submit(request(type: type, id: id, line: line, origin: .page, fields: fields))
    }

    /// One line of the engine's stdout.
    public mutating func engineLine(_ data: Data) -> [Output] {
        guard let head = Wire.decode(Wire.Head.self, from: data), let event = head.event else {
            return []
        }
        let line = String(decoding: data, as: UTF8.self)
        if event == "hello" {
            up = true
            var outputs: [Output] = [.shell(line), .page(line)]
            outputs += parked.map { .engine($0.line) }
            parked = []
            return outputs + sendNext()
        }
        var outputs: [Output] = [(head.re ?? "").hasPrefix(Self.shellPrefix) ? .shell(line) : .page(line)]
        if head.isTerminal, let re = head.re, re == inFlight?.id {
            inFlight = nil
            outputs += sendNext()
        }
        return outputs
    }

    /// The engine has gone: nothing held will be answered.
    public mutating func engineExited(message: String) -> [Output] {
        up = false
        inFlight = nil
        waiting = []
        parked = []
        let line = Self.shellEvent("engine_down", ["message": message])
        return [.shell(line), .page(line)]
    }

    /// The page has gone: what it had waiting is dropped and its scan
    /// in flight is canceled. An execute in flight runs on; its journal
    /// is in the oplog.
    public mutating func pageClosed() -> [Output] {
        waiting.removeAll { $0.origin == .page }
        parked.removeAll { $0.origin == .page }
        guard up, let running = inFlight, running.origin == .page, running.type == "scan" else {
            return []
        }
        serial += 1
        let cancel: [String: Any] = ["type": "cancel", "id": "\(Self.shellPrefix)cancel-\(serial)", "target": running.id]
        return [.engine(Wire.line(cancel)!)]
    }

    /// A `{"event":"shell","state":…}` line, the shell's own word to
    /// the page.
    public static func shellEvent(_ state: String, _ fields: [String: Any] = [:]) -> String {
        var object = fields
        object["event"] = "shell"
        object["state"] = state
        return Wire.line(object)!
    }

    private func request(type: String, id: String, line: String, origin: Origin, fields: [String: Any]) -> Request {
        Request(
            id: id, type: type, line: line, origin: origin,
            target: fields["target"] as? String, planID: fields["plan_id"] as? String)
    }

    private func holds(_ id: String) -> Bool {
        inFlight?.id == id || waiting.contains { $0.id == id } || parked.contains { $0.id == id }
    }

    private mutating func submit(_ request: Request) -> [Output] {
        if Self.slotTypes.contains(request.type) {
            guard up, inFlight == nil else {
                waiting.append(request)
                return []
            }
            inFlight = request
            return [.engine(request.line)]
        }
        if request.type == "cancel", let answered = cancelWaiting(request) {
            return answered
        }
        guard up else {
            parked.append(request)
            return []
        }
        return [.engine(request.line)]
    }

    private mutating func sendNext() -> [Output] {
        guard up, inFlight == nil, !waiting.isEmpty else { return [] }
        let request = waiting.removeFirst()
        inFlight = request
        return [.engine(request.line)]
    }

    /// A cancel whose target is still waiting here: the engine has
    /// never heard of the target and would call the cancel done while
    /// the target went on to run. The target is dropped and both are
    /// answered as the engine answers a canceled request. Only the side
    /// that sent a request can cancel it this way.
    private mutating func cancelWaiting(_ cancel: Request) -> [Output]? {
        guard let target = cancel.target,
            let index = waiting.firstIndex(where: {
                $0.id == target && $0.origin == cancel.origin && $0.type != "tick"
            })
        else { return nil }
        let dropped = waiting.remove(at: index)

        var done: [String: Any] = ["event": "done", "re": dropped.id, "canceled": true]
        if dropped.type == "scan" {
            done["elapsed_ms"] = 0
        } else {
            done["result"] = [
                "run_id": dropped.planID ?? "", "done": 0, "failed": 0, "bytes": 0,
                "trash_bytes": 0, "staged_bytes": 0, "pruned": ["files": 0, "bytes": 0],
                "stopped": true,
            ]
        }
        let answers = [Wire.line(done)!, Wire.line(["event": "done", "re": cancel.id])!]
        return answers.map { cancel.origin == .page ? .page($0) : .shell($0) }
    }

    private static func error(re: String?, code: String, message: String) -> String {
        var object: [String: Any] = ["event": "error", "code": code, "message": message]
        if let re {
            object["re"] = re
        }
        return Wire.line(object)!
    }
}
