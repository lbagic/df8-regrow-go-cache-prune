import AppKit
import RegrowKit
import ServiceManagement

/// The app behind the menu: it keeps one engine running, ticks, routes
/// between the engine and the page, and holds what the menu shows.
@MainActor
final class Shell: ObservableObject {
    static let shared = Shell()

    private static let autotrimKey = "autotrim"
    private static let loginItemKey = "loginItemRegistered"
    private static let tickPrefix = "\(Router.shellPrefix)tick-"

    @Published private(set) var tick: Wire.Tick?
    @Published private(set) var checkedAt: Date?
    @Published private(set) var tickError: String?
    @Published private(set) var hello: Wire.Hello?
    @Published private(set) var environmentFailure: String?
    @Published private(set) var engineDown: String?
    @Published var autotrim: Bool {
        didSet {
            guard autotrim != oldValue else { return }
            UserDefaults.standard.set(autotrim, forKey: Self.autotrimKey)
            log("autotrim turned \(autotrim ? "on" : "off")")
            watch?.tickNow()
        }
    }

    private let config = Config.load()
    private let log: LogFile
    private let notifier: Notifier

    private var router = Router()
    private var supervisor: EngineSupervisor?
    private var watch: Watch?
    private var window: ScanWindow?
    private var stateDirectory: URL?
    private var helloLine: String?
    private var tickLine: String?
    private var downLine: String?
    private var lockNoticed = false
    private var stopping = false
    private var instanceLock: InstanceLock?
    private var askingFinder = false

    private init() {
        log = LogFile(url: config.log)
        notifier = Notifier(log: log)
        autotrim = UserDefaults.standard.bool(forKey: Self.autotrimKey)
    }

    // MARK: What the menu shows

    var barTitle: String {
        tick.map { humanBytes($0.free) } ?? "…"
    }

    /// The engine's words for why autotrim did not run.
    var autotrimNote: String? {
        guard autotrim, let skipped = tick?.pruned?.skipped, !skipped.isEmpty else { return nil }
        return skipped
    }

    var stagedBytes: Int64 {
        tick?.stagedBytes ?? 0
    }

    // MARK: Launch

    func start() {
        log("launched from \(Bundle.main.bundlePath), pid \(getpid())")
        if Config.installed {
            notifier.askPermission()
            registerLoginItem()
        } else {
            log("not ~/Applications/Regrow.app: asking for no notification permission and registering no login item")
        }
        let log = self.log
        DispatchQueue.global(qos: .userInitiated).async {
            let began = Date()
            let captured = LoginEnvironment.capture()
            let answered = Date()
            log("the login shell answered in \(Self.milliseconds(since: began)) ms")
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    let waited = Self.milliseconds(since: answered)
                    if waited >= 1000 {
                        log("the main thread took \(waited) ms to take the login shell's answer")
                    }
                    self.environmentRead(captured)
                }
            }
        }
        if config.openScan {
            openScan()
        }
    }

    private nonisolated static func milliseconds(since start: Date) -> Int {
        Int(Date().timeIntervalSince(start) * 1000)
    }

    /// A login item the owner removed in System Settings stays removed:
    /// the app registers itself once.
    private func registerLoginItem() {
        guard !UserDefaults.standard.bool(forKey: Self.loginItemKey) else { return }
        do {
            try SMAppService.mainApp.register()
            UserDefaults.standard.set(true, forKey: Self.loginItemKey)
            log("registered as a login item")
        } catch {
            log("could not register as a login item: \(error.localizedDescription)")
        }
    }

    private func environmentRead(_ captured: Result<[String: String], LoginEnvironment.Failure>) {
        let base: [String: String]
        switch captured {
        case .success(let environment):
            base = environment
            log("environment captured from \(LoginEnvironment.loginShell()): \(environment.count) variables")
        case .failure(let failure):
            base = ProcessInfo.processInfo.environment
            environmentFailure = failure.reason
            log("environment not captured: \(failure.reason)")
            window?.deliver(environmentLine(failure.reason))
        }
        let environment = config.engineEnvironment(over: base)
        let state = PidFile.stateDirectory(environment: environment)
        stateDirectory = state

        let engine = config.engine
        let app = Bundle.main.executablePath ?? CommandLine.arguments[0]
        let log = self.log
        DispatchQueue.global(qos: .userInitiated).async {
            let lock: InstanceLock?
            do {
                lock = try InstanceLock.acquire(in: state)
                if lock == nil {
                    log("regrow is already running on \(state.path); this one quits")
                    // This copy holds nothing to stop, so a signal ends it
                    // at once, also while nobody answers the alert.
                    signal(SIGTERM, SIG_DFL)
                    signal(SIGINT, SIG_DFL)
                    MainRunLoop.perform {
                        let alert = NSAlert()
                        alert.messageText = "regrow is already running"
                        alert.informativeText = "Another copy of regrow uses \(state.path), so this one quits."
                        NSApp.activate(ignoringOtherApps: true)
                        alert.runModal()
                        exit(0)
                    }
                    return
                }
                Self.clearEarlierRun(state: state, engine: engine, app: app, log: log)
            } catch {
                lock = nil
                log("no single-instance check, so no engine an earlier run left is stopped: \(error)")
            }
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    self.instanceLock = lock
                    self.startEngine(environment)
                }
            }
        }
    }

    /// Stops an engine that outlived the app that started it. Called with
    /// the instance lock held; the app the pid file names can still run
    /// all the same if it ran without the lock, and then keeps its engine.
    private nonisolated static func clearEarlierRun(state: URL, engine: String, app: String, log: LogFile) {
        guard let earlier = PidFile.read(from: state), earlier.app != getpid() else { return }
        if EarlierProcess.isRunning(earlier.app, executable: app) {
            log("the app the pid file names, pid \(earlier.app), still runs; its engine is left alone")
            return
        }
        switch EarlierProcess.stop(earlier.engine, executable: engine) {
        case .notOurs:
            break
        case .stopped(let rung):
            log("an engine left by an earlier run, pid \(earlier.engine), was stopped by \(rung.rawValue)")
        case .survived:
            log("an engine left by an earlier run, pid \(earlier.engine), is still running after SIGKILL")
        }
    }

    private func startEngine(_ environment: [String: String]) {
        guard !stopping else { return }
        let supervisor = EngineSupervisor(
            launch: .init(
                executable: config.engine, arguments: config.engineArguments,
                environment: environment, log: config.engineLog),
            queue: .main,
            onStart: { pid in MainActor.assumeIsolated { self.engineStarted(pid) } },
            onLine: { line in MainActor.assumeIsolated { self.engineLine(line) } },
            onDown: { down in MainActor.assumeIsolated { self.engineWentDown(down) } })
        self.supervisor = supervisor
        supervisor.start()
    }

    // MARK: The engine

    private func engineStarted(_ pid: pid_t) {
        log("engine started: \(config.engine) \(config.engineArguments.joined(separator: " ")), pid \(pid)")
        guard let stateDirectory else { return }
        do {
            try PidFile(app: getpid(), engine: pid).write(to: stateDirectory)
        } catch {
            log("could not write \(stateDirectory.path)/\(PidFile.name): \(error.localizedDescription)")
        }
    }

    private func engineLine(_ line: Data) {
        guard let head = Wire.decode(Wire.Head.self, from: line) else {
            log("the engine wrote a line that is not an event (\(line.count) bytes)")
            return
        }
        if head.isTerminal {
            let code = head.code.map { " \($0): \(head.message ?? "")" } ?? ""
            log("← \(head.event ?? "") re=\(head.re ?? "-")\(code)")
        }
        apply(router.engineEvent(head, line: String(decoding: line, as: UTF8.self)))
    }

    private func engineWentDown(_ down: EngineSupervisor.Down) {
        log("engine down: \(down.message); starting it again in \(Int(down.retryIn)) s")
        engineDown = down.reason
        helloLine = nil
        watch?.settled(dropped: true)
        let outputs = router.engineExited(message: down.message)
        if case .page(let line)? = outputs.last {
            downLine = line
        }
        apply(outputs)
    }

    private func apply(_ outputs: [Router.Output]) {
        for output in outputs {
            switch output {
            case .engine(let line):
                let request = Wire.decode(Wire.Request.self, from: line)
                // Quit has begun: what was waiting stays unsent, rather than
                // start in an engine that is about to be stopped.
                guard !stopping else {
                    log("not sent, quitting: \(request?.type ?? "a request") id=\(request?.id ?? "-")")
                    continue
                }
                if let request {
                    log("→ \(request.type) id=\(request.id)")
                }
                supervisor?.send(line)
            case .page(let line):
                window?.deliver(line)
            case .shell(let line):
                shellEvent(line)
            case .command(let name):
                pageCommand(name)
            }
        }
    }

    private func shellEvent(_ line: String) {
        guard let head = Wire.decode(Wire.Head.self, from: line) else { return }
        let ofTick = head.re?.hasPrefix(Self.tickPrefix) ?? false
        switch head.event {
        case "hello":
            guard let hello = Wire.decode(Wire.Hello.self, from: line) else { return }
            self.hello = hello
            helloLine = line
            engineDown = nil
            downLine = nil
            log("hello: protocol \(hello.protocol), engine \(hello.version), fda \(hello.fda)")
            startWatch()
        case "tick":
            guard let tick = Wire.decode(Wire.Tick.self, from: line) else { return }
            self.tick = tick
            checkedAt = Date()
            tickLine = line
            log("tick: \(humanBytes(tick.free)) free, \(tick.alerts?.count ?? 0) alerts")
            for notice in AlertText.notices(for: tick) {
                notify(notice)
            }
            window?.deliver(line)
        case "done" where ofTick:
            tickError = nil
            lockNoticed = false
            watch?.settled()
        case "error" where ofTick:
            tickError = MenuText.tickError(code: head.code, message: head.message)
            // A locked gate fails every tick while headroom stays low:
            // it is news once, not every quarter of an hour.
            if let notice = AlertText.notice(forError: head.code ?? "", message: head.message ?? "") {
                if !lockNoticed {
                    notify(notice)
                }
                lockNoticed = true
            } else {
                lockNoticed = false
            }
            watch?.settled()
        default:
            break
        }
    }

    /// A copy outside `~/Applications` shares the installed app's bundle
    /// id, and with it the permission to notify: it only logs.
    private func notify(_ notice: Notice) {
        guard Config.installed else {
            log("not ~/Applications/Regrow.app, so no notification: \(notice.title)")
            return
        }
        notifier.post(notice)
    }

    private func startWatch() {
        if let watch {
            watch.resume()
            return
        }
        let watch = Watch(
            queue: .main,
            autotrim: { MainActor.assumeIsolated { self.autotrim } },
            send: { id, autotrim in
                MainActor.assumeIsolated {
                    self.apply(self.router.shellRequest(["type": "tick", "id": id, "autotrim": autotrim]))
                }
            })
        self.watch = watch
        watch.start()
    }

    func woke() {
        log("woke")
        watch?.resume()
    }

    // MARK: The page

    func openScan() {
        if let window {
            window.show()
            return
        }
        guard FileManager.default.fileExists(atPath: config.page.path) else {
            log("no scan page at \(config.page.path)")
            let alert = NSAlert()
            alert.messageText = "The scan page is missing"
            alert.informativeText = "This copy of regrow has no page at \(config.page.path)."
            NSApp.activate(ignoringOtherApps: true)
            alert.runModal()
            return
        }
        let window = ScanWindow(
            page: config.page, log: log,
            onMessage: { [weak self] body in
                guard let self else { return }
                self.apply(self.router.pageMessage(body))
            },
            onPageGone: { [weak self] in
                guard let self else { return }
                self.apply(self.router.pageClosed())
            },
            onClose: { [weak self] in
                guard let self else { return }
                self.log("scan window closed")
                // Released after AppKit has finished closing it.
                let closing = self.window
                self.window = nil
                DispatchQueue.main.async { _ = closing }
            })
        self.window = window
        window.show()
        log("scan window opened, page \(config.page.path)")
    }

    private func pageCommand(_ name: String) {
        switch name {
        case "ready":
            log("page ready")
            var standing = [helloLine, tickLine, downLine].compactMap { $0 }
            if let environmentFailure {
                standing.append(environmentLine(environmentFailure))
            }
            standing.forEach { window?.deliver($0) }
        case "automation":
            // One question at a time: macOS's dialog may still be open, and
            // its answer reaches whichever page is open then.
            guard !askingFinder else {
                log("still waiting for Finder's answer")
                return
            }
            askingFinder = true
            log("asking Finder whether regrow may control it")
            Automation.askFinder(queue: .main) { allowed in
                MainActor.assumeIsolated {
                    self.askingFinder = false
                    self.log("automation \(allowed ? "allowed" : "denied")")
                    self.window?.deliver(Router.shellEvent("automation", ["allowed": allowed]))
                }
            }
        case "open-full-disk-access":
            openFullDiskAccess()
        case "relaunch":
            relaunch()
        default:
            log("the page sent a shell message this app does not know: \(name)")
        }
    }

    private func environmentLine(_ reason: String) -> String {
        Router.shellEvent("environment", ["captured": false, "reason": reason])
    }

    // MARK: What the menu does

    func openFullDiskAccess() {
        log("opening Full Disk Access settings")
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles") {
            NSWorkspace.shared.open(url)
        }
    }

    func showStaging() {
        guard let stateDirectory else { return }
        NSWorkspace.shared.activateFileViewerSelecting([stateDirectory.appendingPathComponent("staging")])
    }

    /// Quits, and has a helper open the bundle again once this process
    /// has gone. The helper gives up waiting after a minute.
    func relaunch() {
        let bundle = Bundle.main.bundlePath
        guard bundle.hasSuffix(".app") else {
            log("relaunch asked for outside a bundle: quit and start it again by hand")
            return
        }
        let wait = #"i=0; while kill -0 "$1" 2>/dev/null && [ "$i" -lt 600 ]; do sleep 0.1; i=$((i+1)); done; exec /usr/bin/open "$2""#
        let null = open("/dev/null", O_RDWR | O_CLOEXEC)
        defer { close(null) }
        do {
            _ = try Spawn.start(
                "/bin/sh", arguments: ["-c", wait, "sh", "\(getpid())", bundle],
                environment: ProcessInfo.processInfo.environment,
                stdin: null, stdout: null, stderr: null, group: .session)
            log("relaunching")
            MainRunLoop.quit()
        } catch {
            log("could not relaunch: \(error)")
        }
    }

    // MARK: Quit

    /// Runs the stop ladder on the engine, then calls `done` on the
    /// main thread.
    func shutdown(_ done: @escaping @MainActor () -> Void) {
        guard !stopping else { return }
        stopping = true
        watch?.stop()
        let executing = router.executeInFlight
        log(executing ? "quitting while an execute is in flight" : "quitting")

        let supervisor = self.supervisor
        let state = stateDirectory
        let log = self.log
        DispatchQueue.global(qos: .userInitiated).async {
            if let rung = supervisor?.stop(executeInFlight: executing) {
                log("engine stopped by \(rung.rawValue)")
            }
            // An engine that is somehow still there keeps its pid file,
            // so the next launch finds it.
            if let state, supervisor?.isRunning != true, PidFile.read(from: state)?.app == getpid() {
                PidFile.remove(from: state)
            }
            log("quit")
            MainRunLoop.perform(done)
        }
    }
}
