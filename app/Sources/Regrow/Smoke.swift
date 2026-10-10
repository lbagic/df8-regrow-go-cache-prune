import AppKit
import RegrowKit

/// The two smoke modes: the same executable runs one `SmokeRun`
/// sequence, shows no menu, and exits 0 when every stage passed.
enum Smoke {
    enum Mode {
        case trash
        case alert
    }

    static func run(_ mode: Mode, directory: String) -> Never {
        let config = Config.load()
        let directory = URL(fileURLWithPath: directory)
        let notifier = Notifier(log: LogFile(url: directory.appendingPathComponent("smoke.log")))
        let run = SmokeRun(
            directory: directory, engine: config.engine,
            hooks: .init(
                environment: { environment(config) },
                askFinder: askFinder,
                notificationsAllowed: { notificationsAllowed(notifier) },
                post: { notice in
                    waitFor(30) { done in notifier.post(notice) { done($0) } } ?? false
                }))
        Thread.detachNewThread {
            let ok: Bool
            switch mode {
            case .trash: ok = run.trash()
            case .alert: ok = run.alert(engineArguments: config.engineArguments)
            }
            exit(ok ? 0 : 1)
        }
        // The notification centre and the Automation dialog want an app
        // that is running its event loop.
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory)
        app.run()
        exit(1)
    }

    /// The environment the app would start its engine with.
    private static func environment(_ config: Config) -> (variables: [String: String], failure: String?) {
        switch LoginEnvironment.capture() {
        case .success(let environment):
            return (config.engineEnvironment(over: environment), nil)
        case .failure(let failure):
            return (config.engineEnvironment(over: ProcessInfo.processInfo.environment), failure.reason)
        }
    }

    /// No time limit: the owner may be reading the dialog.
    private static func askFinder() -> Bool {
        let asked = DispatchSemaphore(value: 0)
        var allowed = false
        Automation.askFinder(queue: DispatchQueue(label: "regrow.smoke.automation")) {
            allowed = $0
            asked.signal()
        }
        asked.wait()
        return allowed
    }

    /// The owner is asked only from the installed bundle, as the app
    /// asks, and has two minutes to answer.
    private static func notificationsAllowed(_ notifier: Notifier) -> (allowed: Bool, status: String) {
        let read = waitFor(30) { done in notifier.permission { allowed, status in done((allowed, status)) } }
        guard let (allowed, status) = read else {
            return (false, "the notification centre did not answer")
        }
        if let allowed {
            return (allowed, status)
        }
        guard Config.installed else {
            return (false, "\(status), and only ~/Applications/Regrow.app asks")
        }
        let granted = waitFor(120) { done in notifier.askPermission { done($0) } }
        return (granted ?? false, granted == nil ? "the question was not answered" : "denied")
    }

    /// The value `start` hands to its callback, nil after `seconds`.
    private static func waitFor<T>(_ seconds: TimeInterval, _ start: (@escaping (T) -> Void) -> Void) -> T? {
        let answer = Mailbox<T>()
        start { answer.put($0) }
        return answer.take(until: Date().addingTimeInterval(seconds))
    }
}
