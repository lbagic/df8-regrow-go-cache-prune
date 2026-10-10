import Foundation

/// The menu's lines about the last tick, as text.
public enum MenuText {
    public static func free(_ tick: Wire.Tick) -> String {
        "\(humanBytes(tick.free)) free of \(humanBytes(tick.total))"
    }

    public static func purgeable(_ tick: Wire.Tick) -> String {
        "Purgeable: \(humanBytes(tick.purgeable))"
    }

    public static func swap(_ tick: Wire.Tick) -> String {
        "Swap: \(humanBytes(tick.swapUsed))"
    }

    /// Days to full, or why there is no such figure.
    public static func forecast(_ tick: Wire.Tick) -> String {
        if let days = tick.daysToFull {
            if days < 1 {
                return "Full in less than a day at this rate"
            }
            let figure = String(format: "%.1f", locale: Locale(identifier: "en_US_POSIX"), days)
            return "Full in about \(figure) days at this rate"
        }
        if tick.historyUnreadable == true {
            return "No forecast: the free-space history could not be read"
        }
        switch tick.noForecast {
        case "short_history": return "No forecast yet: not enough history"
        case "not_falling": return "No forecast: free space is not falling"
        default: return "No forecast"
        }
    }

    public static func fullDiskAccess(_ fda: String?) -> String {
        switch fda {
        case "granted": return "Full Disk Access: granted"
        case "denied": return "Full Disk Access: not granted"
        case nil: return "Full Disk Access: not checked yet"
        default: return "Full Disk Access: unknown"
        }
    }
}
