import Foundation

/// One notification to post.
public struct Notice: Equatable, Sendable {
    public let title: String
    public let body: String

    public init(title: String, body: String) {
        self.title = title
        self.body = body
    }
}

/// What a tick has to say to someone who is not looking at the menu.
public enum AlertText {
    /// One notice per alert, in the engine's order and with the
    /// engine's words, then one when autotrim deleted files.
    public static func notices(for tick: Wire.Tick) -> [Notice] {
        var notices = (tick.alerts ?? []).map { alert in
            Notice(title: title(forKind: alert.kind), body: alert.message)
        }
        if let pruned = tick.pruned, let files = pruned.files, files > 0 {
            let freed = humanBytes(pruned.bytes ?? 0)
            var body = "Deleted \(files) unused \(files == 1 ? "file" : "files") from \(pruned.ruleId)."
            if let free = pruned.freeAfter {
                body += " \(humanBytes(free)) free now."
            }
            notices.append(Notice(title: "Autotrim freed \(freed)", body: body))
        }
        return notices
    }

    /// The notice for a tick's terminal error, when it has one: a
    /// locked gate refuses a prune exactly when headroom is low.
    public static func notice(forError code: String, message: String) -> Notice? {
        guard code == "autotrim_locked" else { return nil }
        return Notice(title: "Autotrim is locked", body: message)
    }

    static func title(forKind kind: String) -> String {
        switch kind {
        case "free-below": return "Disk space is low"
        case "days-to-full": return "The disk is filling up"
        case "swap-up": return "Swap is growing fast"
        case "free-fall": return "Free space is falling fast"
        default: return "regrow"
        }
    }
}
