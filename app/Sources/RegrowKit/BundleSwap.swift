import Foundation

/// Puts a staged bundle at its destination in one step, so a reader of
/// the destination sees the old bundle or the new one, never a mix.
public enum BundleSwap {
    public struct Failure: Error, CustomStringConvertible {
        public let description: String
    }

    /// The two paths `--swap` renames: `~/Applications/.Regrow.app.staged`
    /// and `~/Applications/Regrow.app`, as written, never resolved. Run
    /// through LaunchServices the app holds the owner's grants, and a
    /// link in a resolved path can be re-pointed between a check and the
    /// rename; `rename` does not follow a link in a path's last part, so
    /// only entries of these two names can move. The running bundle must
    /// resolve where the staged path resolves, so a link planted at the
    /// staged path lets any bundle through, the installed one included.
    public static func check(
        staged: URL, destination: URL, running: URL, home: URL = Account.home
    ) throws -> (staged: URL, destination: URL) {
        let applications = home.appendingPathComponent("Applications")
        let pair = (
            staged: applications.appendingPathComponent(".Regrow.app.staged"),
            destination: applications.appendingPathComponent("Regrow.app")
        )
        guard staged.standardizedFileURL.path == pair.staged.path,
            destination.standardizedFileURL.path == pair.destination.path
        else {
            throw Failure(description: "--swap takes \(pair.staged.path) and \(pair.destination.path)")
        }
        guard running.standardizedFileURL.resolvingSymlinksInPath().path
            == pair.staged.resolvingSymlinksInPath().path
        else {
            throw Failure(description: "\(running.path) is not \(pair.staged.path)")
        }
        return pair
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
