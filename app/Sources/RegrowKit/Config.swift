import Foundation

/// Where this run finds its engine, its page and its logs. A run whose
/// code is ad hoc or unsigned (a build-folder bundle, `swift run`) takes
/// each `REGROW_` variable of its own environment in place of one
/// default; a sealed run (`CodeSignature`) reads none, since it holds
/// the owner's grants and anything can set its environment.
public struct Config: Equatable {
    public let engine: String
    public let page: URL
    public let rulesDir: String?
    public let home: String?
    public let stateHome: String?
    public let log: URL
    /// The smoke test's one rule, which no variable replaces.
    public let smokeRules: URL
    public let openScan: Bool

    public static func load(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        arguments: [String] = CommandLine.arguments,
        executable: URL? = Bundle.main.executableURL,
        resources: URL? = Bundle.main.resourceURL,
        home: URL = Account.home,
        sealed: Bool = CodeSignature.runningSealed
    ) -> Config {
        func named(_ name: String) -> String? {
            guard !sealed else { return nil }
            return environment[name].flatMap { $0.isEmpty ? nil : $0 }
        }
        let executable = executable ?? URL(fileURLWithPath: arguments[0])
        let resources = resources ?? executable.deletingLastPathComponent()
        let logs = home.appendingPathComponent("Library/Logs/regrow/app.log")
        return Config(
            engine: named("REGROW_ENGINE")
                ?? executable.deletingLastPathComponent().appendingPathComponent("regrow-engine").path,
            page: named("REGROW_PAGE").map { URL(fileURLWithPath: $0) }
                ?? resources.appendingPathComponent("page.html"),
            rulesDir: named("REGROW_RULES_DIR"),
            home: named("REGROW_HOME"),
            stateHome: named("REGROW_STATE_HOME"),
            log: named("REGROW_LOG").map { URL(fileURLWithPath: $0) } ?? logs,
            smokeRules: resources.appendingPathComponent("smoke"),
            openScan: arguments.contains("--open-scan"))
    }

    /// The engine's stderr, beside the app's log.
    public var engineLog: URL {
        log.deletingLastPathComponent().appendingPathComponent("engine.log")
    }

    public var engineArguments: [String] {
        ["engine"] + (rulesDir.map { ["--rules-dir", $0] } ?? [])
    }

    public func engineEnvironment(over base: [String: String]) -> [String: String] {
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
    public static var installed: Bool {
        let expected = Account.home.appendingPathComponent("Applications/Regrow.app")
        return Bundle.main.bundleURL.resolvingSymlinksInPath().path == expected.resolvingSymlinksInPath().path
    }
}
