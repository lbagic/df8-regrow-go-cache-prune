import Foundation
import RegrowKit
import Testing

@Suite("Sizes")
struct SizesTests {
    @Test("sizes print as the engine prints them", arguments: [
        (Int64(0), "0 B"),
        (1023, "1023 B"),
        (1024, "1.0 KiB"),
        (1_572_864, "1.5 MiB"),
        (34_359_738_368, "32.0 GiB"),
    ])
    func sizes(bytes: Int64, text: String) {
        #expect(humanBytes(bytes) == text)
    }
}

@Suite("Line framing")
struct LineFramerTests {
    @Test("a line split across chunks comes out whole, and an empty line is a line")
    func chunkBoundaries() {
        var framer = LineFramer()
        #expect(framer.append(Data("ab".utf8)) == [])
        #expect(framer.append(Data("c\nde".utf8)) == [Data("abc".utf8)])
        #expect(framer.append(Data("\n\nf".utf8)) == [Data("de".utf8), Data()])
        #expect(framer.pending == 1)
        #expect(framer.append(Data("\n".utf8)) == [Data("f".utf8)])
        #expect(framer.pending == 0)
    }

    @Test("a 2 MiB line arriving in small reads is one line with every byte")
    func twoMebibyteLine() {
        let size = 2 << 20
        var line = Data(count: size)
        for index in stride(from: 0, to: size, by: 4099) {
            line[index] = UInt8(ascii: "x")
        }
        line = Data(line.map { $0 == 0 ? UInt8(ascii: "-") : $0 })
        let stream = line + Data("\nnext\n".utf8)

        var framer = LineFramer()
        var lines: [Data] = []
        var offset = 0
        while offset < stream.count {
            let end = min(offset + 1000, stream.count)
            lines += framer.append(stream.subdata(in: offset..<end))
            offset = end
        }
        #expect(lines.count == 2)
        #expect(lines.first == line)
        #expect(lines.last == Data("next".utf8))
        #expect(framer.pending == 0)
    }
}

@Suite("Bundle swap")
struct BundleSwapTests {
    private func bundle(_ name: String, holding marker: String, in directory: URL) throws -> URL {
        let url = directory.appendingPathComponent(name)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        try Data(marker.utf8).write(to: url.appendingPathComponent("marker"))
        return url
    }

    private func marker(in bundle: URL) -> String? {
        try? String(contentsOf: bundle.appendingPathComponent("marker"), encoding: .utf8)
    }

    @Test("with a destination in place the two bundles trade names")
    func swapsTwoDirectories() async throws {
        try await withTemporaryDirectory { directory in
            let staged = try bundle("staged.app", holding: "new", in: directory)
            let destination = try bundle("Installed.app", holding: "old", in: directory)

            try BundleSwap.swap(staged: staged, destination: destination)

            #expect(marker(in: destination) == "new")
            #expect(marker(in: staged) == "old")
        }
    }

    @Test("with no destination the staged bundle is renamed into place")
    func renamesWithoutADestination() async throws {
        try await withTemporaryDirectory { directory in
            let staged = try bundle("staged.app", holding: "new", in: directory)
            let destination = directory.appendingPathComponent("Installed.app")

            try BundleSwap.swap(staged: staged, destination: destination)

            #expect(marker(in: destination) == "new")
            #expect(!FileManager.default.fileExists(atPath: staged.path))
        }
    }

    @Test("a staged bundle that is not there is an error that names both paths")
    func missingStagedBundle() async throws {
        try await withTemporaryDirectory { directory in
            let staged = directory.appendingPathComponent("staged.app")
            let destination = directory.appendingPathComponent("Installed.app")
            #expect(throws: BundleSwap.Failure.self) {
                try BundleSwap.swap(staged: staged, destination: destination)
            }
        }
    }
}

@Suite("Alert text")
struct AlertTextTests {
    private func tick(_ extra: String) throws -> Wire.Tick {
        let line = #"{"event":"tick","re":"shell-tick-1","at":"2026-01-01T00:00:00Z","total":494384795648,"free":9663676416,"purgeable":0,"swap_used":0\#(extra)}"#
        return try #require(Wire.decode(Wire.Tick.self, from: line))
    }

    @Test("each alert kind has its own title and the engine's message as its body", arguments: [
        ("free-below", "Disk space is low"),
        ("days-to-full", "The disk is filling up"),
        ("swap-up", "Swap is growing fast"),
        ("free-fall", "Free space is falling fast"),
    ])
    func alertKinds(kind: String, title: String) throws {
        let tick = try tick(#","alerts":[{"kind":"\#(kind)","message":"The engine's words."}]"#)
        #expect(AlertText.notices(for: tick) == [Notice(title: title, body: "The engine's words.")])
    }

    @Test("several alerts become several notices, in the engine's order")
    func severalAlerts() throws {
        let tick = try tick(
            #","alerts":[{"kind":"free-fall","message":"A.","acute":true},{"kind":"free-below","message":"B.","band":10737418240}]"#)
        #expect(AlertText.notices(for: tick).map(\.body) == ["A.", "B."])
    }

    @Test("a prune that deleted files is a notice with what it freed")
    func prune() throws {
        let tick = try tick(
            #","pruned":{"rule_id":"go-build-cache","run":"r1","files":1200,"bytes":1610612736,"free_before":9663676416,"free_after":11274289152}"#)
        #expect(AlertText.notices(for: tick) == [
            Notice(title: "Autotrim freed 1.5 GiB", body: "Deleted 1200 unused files from go-build-cache. 10.5 GiB free now.")
        ])
    }

    @Test("a prune that was skipped posts nothing: its reason belongs to the menu")
    func skippedPrune() throws {
        let tick = try tick(
            #","pruned":{"rule_id":"go-build-cache","skipped":"autotrim is locked","files":0,"bytes":0,"free_before":0,"free_after":0}"#)
        #expect(AlertText.notices(for: tick).isEmpty)
        #expect(tick.pruned?.skipped == "autotrim is locked")
    }

    @Test("a locked gate is a notice with the engine's message; other tick errors are not")
    func lockedGate() {
        #expect(
            AlertText.notice(forError: "autotrim_locked", message: "Run regrow prune go-build --yes once.")
                == Notice(title: "Autotrim is locked", body: "Run regrow prune go-build --yes once."))
        #expect(AlertText.notice(forError: "tick_failed", message: "no sample") == nil)
    }

    @Test("a tick with nothing to say posts nothing")
    func quietTick() throws {
        #expect(AlertText.notices(for: try tick("")).isEmpty)
    }
}

@Suite("Menu text")
struct MenuTextTests {
    private func tick(_ extra: String) throws -> Wire.Tick {
        let line = #"{"event":"tick","total":494384795648,"free":34359738368,"purgeable":536870912,"swap_used":1073741824\#(extra)}"#
        return try #require(Wire.decode(Wire.Tick.self, from: line))
    }

    @Test("the figures of a tick read as the menu shows them")
    func figures() throws {
        let tick = try tick("")
        #expect(MenuText.free(tick) == "32.0 GiB free of 460.4 GiB")
        #expect(MenuText.purgeable(tick) == "Purgeable: 512.0 MiB")
        #expect(MenuText.swap(tick) == "Swap: 1.0 GiB")
    }

    @Test("days to full, or the reason there is no forecast", arguments: [
        (#","days_to_full":12.34"#, "Full in about 12.3 days at this rate"),
        (#","days_to_full":0.4"#, "Full in less than a day at this rate"),
        (#","no_forecast":"short_history""#, "No forecast yet: not enough history"),
        (#","no_forecast":"not_falling""#, "No forecast: free space is not falling"),
        (#","history_unreadable":true"#, "No forecast: the free-space history could not be read"),
        ("", "No forecast"),
    ])
    func forecast(extra: String, text: String) throws {
        #expect(MenuText.forecast(try tick(extra)) == text)
    }

    @Test("a locked gate reads as autotrim not running, any other tick error as a failed check")
    func tickError() {
        #expect(
            MenuText.tickError(code: "autotrim_locked", message: "autotrim is locked for go-build-cache")
                == "Autotrim did not run: autotrim is locked for go-build-cache")
        #expect(
            MenuText.tickError(code: "tick_failed", message: "statfs: input/output error")
                == "Last check failed: statfs: input/output error")
        #expect(MenuText.tickError(code: "tick_failed", message: nil) == "Last check failed: tick_failed")
    }

    @Test("staging bytes are read when the engine sends them")
    func staging() throws {
        #expect(try tick(#","staged_bytes":2048"#).stagedBytes == 2048)
        #expect(try tick("").stagedBytes == nil)
    }
}

@Suite("Watch")
struct WatchTests {
    private final class Clock: @unchecked Sendable {
        var now = Date(timeIntervalSince1970: 1_000_000)
    }

    private final class Sent: @unchecked Sendable {
        var ticks: [String] = []
        var autotrim: [Bool] = []
    }

    private func watch(_ clock: Clock, _ sent: Sent, autotrim: @escaping () -> Bool = { false }) -> Watch {
        Watch(
            interval: 3600, queue: DispatchQueue(label: "watch-test"), now: { clock.now },
            autotrim: autotrim,
            send: { id, autotrim in
                sent.ticks.append(id)
                sent.autotrim.append(autotrim)
            })
    }

    @Test("it ticks at start, and on wake only when the last tick is over a minute old")
    func startAndWake() {
        let clock = Clock()
        let sent = Sent()
        let watch = watch(clock, sent)
        defer { watch.stop() }

        watch.start()
        #expect(sent.ticks == ["shell-tick-1"])
        watch.settled()

        clock.now += 30
        watch.resume()
        #expect(sent.ticks == ["shell-tick-1"])

        clock.now += 31
        watch.resume()
        #expect(sent.ticks == ["shell-tick-1", "shell-tick-2"])
    }

    @Test("one tick is out at a time, and a tick asked for meanwhile follows it")
    func oneAtATime() {
        let clock = Clock()
        let sent = Sent()
        var setting = false
        let watch = watch(clock, sent, autotrim: { setting })
        defer { watch.stop() }

        watch.start()
        clock.now += 120
        watch.resume()
        #expect(sent.ticks.count == 1)

        setting = true
        watch.tickNow()
        #expect(sent.ticks.count == 1)
        watch.settled()
        #expect(sent.ticks.count == 2)
        #expect(sent.autotrim == [false, true])
    }

    @Test("a tick dropped with the engine does not count as the last one")
    func dropped() {
        let clock = Clock()
        let sent = Sent()
        let watch = watch(clock, sent)
        defer { watch.stop() }

        watch.start()
        watch.settled(dropped: true)
        clock.now += 5
        watch.resume()
        #expect(sent.ticks.count == 2)
    }
}

@Suite("Restart delay")
struct RestartDelayTests {
    @Test("the delay doubles to a minute and starts over after an engine that stayed up")
    func grows() {
        var delay = RestartDelay()
        let delays = (0..<8).map { _ in delay.after(uptime: 0.1) }
        #expect(delays == [1, 2, 4, 8, 16, 32, 60, 60])
        #expect(delay.after(uptime: 61) == 1)
        #expect(delay.after(uptime: 0.1) == 2)
    }
}
