import Foundation

/// Cuts a byte stream into lines. Each chunk is searched once, so a
/// line of megabytes arriving in small reads costs its length, not its
/// length squared.
public struct LineFramer {
    private var partial = Data()

    public init() {}

    /// The lines `chunk` completes, without their newline.
    public mutating func append(_ chunk: Data) -> [Data] {
        var lines: [Data] = []
        var rest = chunk[...]
        while let newline = rest.firstIndex(of: 0x0A) {
            partial.append(rest[rest.startIndex..<newline])
            lines.append(partial)
            partial = Data()
            rest = rest[rest.index(after: newline)...]
        }
        partial.append(rest)
        return lines
    }

    /// Bytes of a line whose newline has not arrived.
    public var pending: Int { partial.count }
}
