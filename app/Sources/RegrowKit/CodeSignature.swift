import Foundation
import Security

/// Whether code can hold the owner's grants. macOS keeps Full Disk
/// Access, Finder control and notifications for a bundle id and a
/// signature, wherever the bundle sits: a copy of the installed app at
/// any path holds them too. Ad hoc and unsigned code holds none across
/// a rebuild, so only such code may be pointed at other files.
public enum CodeSignature {
    /// True unless the code at `url` is shown to be signed ad hoc or not
    /// at all: code that cannot be read counts as sealed.
    public static func sealed(at url: URL) -> Bool {
        var code: SecStaticCode?
        guard SecStaticCodeCreateWithPath(url as CFURL, [], &code) == errSecSuccess, let code else {
            return true
        }
        return sealed(code)
    }

    /// The same, for this process.
    public static var runningSealed: Bool {
        var running: SecCode?
        var code: SecStaticCode?
        guard SecCodeCopySelf([], &running) == errSecSuccess, let running,
            SecCodeCopyStaticCode(running, [], &code) == errSecSuccess, let code
        else { return true }
        return sealed(code)
    }

    private static func sealed(_ code: SecStaticCode) -> Bool {
        var read: CFDictionary?
        guard SecCodeCopySigningInformation(code, SecCSFlags(rawValue: kSecCSSigningInformation), &read)
            == errSecSuccess,
            let information = read as? [String: Any]
        else { return true }
        if information[kSecCodeInfoIdentifier as String] == nil {
            return false
        }
        let flags = (information[kSecCodeInfoFlags as String] as? NSNumber)?.uint32Value ?? 0
        return flags & SecCodeSignatureFlags.adhoc.rawValue == 0
    }
}
