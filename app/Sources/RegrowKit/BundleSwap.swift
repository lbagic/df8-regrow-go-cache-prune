import Foundation

/// Puts a staged bundle at its destination in one step, so a reader of
/// the destination sees the old bundle or the new one, never a mix.
public enum BundleSwap {
    public struct Failure: Error, CustomStringConvertible {
        public let description: String
    }

    /// With a destination in place the two trade names, and the old
    /// bundle is left at `staged`. Without one, `staged` is renamed.
    public static func swap(staged: URL, destination: URL) throws {
        var info = stat()
        let exists = lstat(destination.path, &info) == 0
        let result =
            exists
            ? renamex_np(staged.path, destination.path, UInt32(RENAME_SWAP))
            : rename(staged.path, destination.path)
        guard result == 0 else {
            let reason = String(cString: strerror(errno))
            let verb = exists ? "swap" : "rename"
            throw Failure(description: "\(verb) \(staged.path) → \(destination.path): \(reason)")
        }
    }
}
