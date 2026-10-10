import Foundation

/// The fields of the engine protocol the shell itself reads. Everything
/// else on an event line passes through to the page untouched.
public enum Wire {
    /// What routing needs of any event line, and an error's own fields.
    public struct Head: Decodable, Equatable {
        public let event: String?
        public let re: String?
        public let code: String?
        public let message: String?

        public var isTerminal: Bool { event == "done" || event == "error" }
    }

    public struct Hello: Decodable, Equatable {
        public let `protocol`: Int
        public let version: String
        public let fda: String
    }

    public struct Tick: Decodable, Equatable {
        public let total: Int64
        public let free: Int64
        public let purgeable: Int64
        public let swapUsed: Int64
        public let daysToFull: Double?
        public let alerts: [Alert]?
        public let pruned: Pruned?
        public let historyUnreadable: Bool?
        /// Why there is no forecast: `short_history` or `not_falling`.
        public let noForecast: String?
        public let stagedBytes: Int64?
    }

    public struct Alert: Decodable, Equatable {
        public let kind: String
        public let message: String
    }

    /// What autotrim did, or in `skipped` why it did not run.
    public struct Pruned: Decodable, Equatable {
        public let ruleId: String
        public let skipped: String?
        public let files: Int?
        public let bytes: Int64?
        public let freeAfter: Int64?
    }

    public static func decode<T: Decodable>(_ type: T.Type, from line: Data) -> T? {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try? decoder.decode(type, from: line)
    }

    public static func decode<T: Decodable>(_ type: T.Type, from line: String) -> T? {
        decode(type, from: Data(line.utf8))
    }

    /// One JSON object as a line. Keys are sorted so a line reads the
    /// same on every run.
    public static func line(_ object: [String: Any]) -> String? {
        guard JSONSerialization.isValidJSONObject(object),
            let data = try? JSONSerialization.data(
                withJSONObject: object, options: [.sortedKeys, .withoutEscapingSlashes])
        else { return nil }
        return String(decoding: data, as: UTF8.self)
    }
}
