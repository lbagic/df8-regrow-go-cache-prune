import Foundation

/// The user this process runs as, from the password database and
/// `confstr`: whatever the launching process put in the environment
/// (`open --env`, `launchctl setenv`) changes none of it.
public enum Account {
    public static var home: URL {
        if let entry = getpwuid(getuid()), let directory = entry.pointee.pw_dir {
            let path = String(cString: directory)
            if !path.isEmpty {
                return URL(fileURLWithPath: path, isDirectory: true)
            }
        }
        return FileManager.default.homeDirectoryForCurrentUser
    }

    public static var name: String {
        if let entry = getpwuid(getuid()), let name = entry.pointee.pw_name {
            return String(cString: name)
        }
        return NSUserName()
    }

    /// The per-user temp folder, `/var/folders/…/T/`.
    public static var temporaryDirectory: URL {
        var buffer = [CChar](repeating: 0, count: Int(PATH_MAX))
        if confstr(_CS_DARWIN_USER_TEMP_DIR, &buffer, buffer.count) > 0 {
            let path = String(cString: buffer)
            if !path.isEmpty {
                return URL(fileURLWithPath: path, isDirectory: true)
            }
        }
        return URL(fileURLWithPath: "/tmp", isDirectory: true)
    }

    /// What every child of the app starts from: the variables
    /// LaunchServices gives an app, rebuilt here. The app's own
    /// environment reaches no child, since a caller can put `ZDOTDIR`
    /// or `HOME` there for the login shell, or a `PATH` for a tool.
    public static func environment() -> [String: String] {
        let name = self.name
        return [
            "HOME": home.path,
            "USER": name,
            "LOGNAME": name,
            "SHELL": LoginEnvironment.loginShell(),
            "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
            "TMPDIR": temporaryDirectory.path + "/",
        ]
    }
}
