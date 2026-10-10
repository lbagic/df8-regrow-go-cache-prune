import Foundation

/// When the shell ticks: at start, on a timer, and on wake when the
/// last tick is old. One tick is out at a time. Every call is made on
/// the queue the watch was given.
public final class Watch {
    public static let interval: TimeInterval = 15 * 60
    public static let staleAfter: TimeInterval = 60

    private let interval: TimeInterval
    private let queue: DispatchQueue
    private let now: () -> Date
    private let autotrim: () -> Bool
    private let send: (_ id: String, _ autotrim: Bool) -> Void

    private var timer: DispatchSourceTimer?
    private var serial = 0
    private var out = false
    private var again = false
    private var last: Date?
    private var lastBeforeThis: Date?

    /// `autotrim` reads the stored setting as each tick is sent.
    public init(
        interval: TimeInterval = Watch.interval, queue: DispatchQueue,
        now: @escaping () -> Date = Date.init,
        autotrim: @escaping () -> Bool,
        send: @escaping (_ id: String, _ autotrim: Bool) -> Void
    ) {
        self.interval = interval
        self.queue = queue
        self.now = now
        self.autotrim = autotrim
        self.send = send
    }

    /// Ticks now and arms the timer. The timer counts time awake, so a
    /// long sleep does not end in a burst; `resume` covers the wake.
    public func start() {
        tick()
        guard timer == nil else { return }
        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now() + interval, repeating: interval, leeway: .seconds(30))
        timer.setEventHandler { [weak self] in self?.tick() }
        timer.resume()
        self.timer = timer
    }

    /// The machine woke, or the engine came back: ticks when the last
    /// tick is over a minute old.
    public func resume() {
        if let last, now().timeIntervalSince(last) <= Watch.staleAfter {
            return
        }
        tick()
    }

    /// Ticks as soon as the engine can take one: now, or right after
    /// the tick that is out, which was sent with the setting as it was.
    public func tickNow() {
        if out {
            again = true
        } else {
            tick()
        }
    }

    /// The tick that was out got its terminal event, or was `dropped`
    /// with the engine that held it.
    public func settled(dropped: Bool = false) {
        guard out else { return }
        out = false
        if dropped {
            last = lastBeforeThis
            again = false
        } else if again {
            again = false
            tick()
        }
    }

    public func stop() {
        timer?.cancel()
        timer = nil
    }

    private func tick() {
        guard !out else { return }
        out = true
        lastBeforeThis = last
        last = now()
        serial += 1
        send("shell-tick-\(serial)", autotrim())
    }
}
