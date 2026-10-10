import Foundation

/// A byte count as the engine prints it: binary units, one decimal,
/// plain bytes below 1 KiB.
public func humanBytes(_ n: Int64) -> String {
    let unit: Int64 = 1024
    if n < unit {
        return "\(n) B"
    }
    var div = unit
    var exp = 0
    var m = n / unit
    while m >= unit {
        div *= unit
        exp += 1
        m /= unit
    }
    let prefix = String(Array("KMGTPE")[exp])
    return String(
        format: "%.1f %@iB", locale: Locale(identifier: "en_US_POSIX"),
        Double(n) / Double(div), prefix)
}
