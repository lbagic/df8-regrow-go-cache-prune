import Foundation
import RegrowKit

/// Where this run finds its engine, its page and its logs. Each
/// `REGROW_` variable of the app's own environment replaces one default,
/// for a run outside the installed bundle.
struct Config {
    let engine: String
    let page: URL
    let rulesDir: String?
    let home: String?
    let stateHome: String?
    let log: URL
    let openScan: Bool

    static func load(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        arguments: [String] = CommandLine.arguments
    ) -> Config {
        func named(_ name: String) -> String? {
            environment[name].flatMap { $0.isEmpty ? nil : $0 }
        }
        let executable = Bundle.main.executableURL ?? URL(fileURLWithPath: arguments[0])
        let resources = Bundle.main.resourceURL ?? executable.deletingLastPathComponent()
        let logs = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Logs/regrow/app.log")
        return Config(
            engine: named("REGROW_ENGINE")
                ?? executable.deletingLastPathComponent().appendingPathComponent("regrow-engine").path,
            page: named("REGROW_PAGE").map { URL(fileURLWithPath: $0) }
                ?? resources.appendingPathComponent("page.html"),
            rulesDir: named("REGROW_RULES_DIR"),
            home: named("REGROW_HOME"),
            stateHome: named("REGROW_STATE_HOME"),
            log: named("REGROW_LOG").map { URL(fileURLWithPath: $0) } ?? logs,
            openScan: arguments.contains("--open-scan"))
    }

    /// The engine's stderr, beside the app's log.
    var engineLog: URL {
        log.deletingLastPathComponent().appendingPathComponent("engine.log")
    }

    var engineArguments: [String] {
        ["engine"] + (rulesDir.map { ["--rules-dir", $0] } ?? [])
    }

    func engineEnvironment(over base: [String: String]) -> [String: String] {
        var environment = base
        if let home {
            environment["HOME"] = home
        }
        if let stateHome {
            environment["XDG_STATE_HOME"] = stateHome
        }
        return environment
    }

    /// True for `~/Applications/Regrow.app` alone: the one bundle that
    /// may ask for notifications and register itself as a login item.
    static var installed: Bool {
        let expected = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Applications/Regrow.app")
        return Bundle.main.bundleURL.resolvingSymlinksInPath().path == expected.resolvingSymlinksInPath().path
    }
}
