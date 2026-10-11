import AppKit
import RegrowKit
import SwiftUI

/// The menu bar item is the app's only scene, so nothing opens at
/// launch.
struct RegrowApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @ObservedObject private var shell = Shell.shared

    var body: some Scene {
        MenuBarExtra {
            MenuView(shell: shell)
        } label: {
            HStack(spacing: 4) {
                Image(systemName: "internaldrive")
                Text(shell.barTitle)
            }
        }
        .menuBarExtraStyle(.menu)
    }
}

/// System controls and text only, top to bottom as the owner reads it.
struct MenuView: View {
    @ObservedObject var shell: Shell

    private static let clock: DateFormatter = {
        let formatter = DateFormatter()
        formatter.dateStyle = .none
        formatter.timeStyle = .short
        return formatter
    }()

    var body: some View {
        if let tick = shell.tick {
            Text(MenuText.free(tick))
            Text(MenuText.purgeable(tick))
            Text(MenuText.swap(tick))
            Text(MenuText.forecast(tick))
        }
        if let checkedAt = shell.checkedAt {
            Text("Checked at \(Self.clock.string(from: checkedAt))")
        } else {
            Text("Not checked yet")
        }
        if let error = shell.tickError {
            Text(error)
        }
        if let down = shell.engineDown {
            Text("The engine is not running: \(down)")
        }

        Divider()
        Toggle("Trim the Go build cache when space is low", isOn: $shell.autotrim)
        if let note = shell.autotrimNote {
            Text(note)
        }

        Divider()
        Text(MenuText.fullDiskAccess(shell.hello?.fda))
        if let fda = shell.hello?.fda, fda != "granted" {
            Button("Open Full Disk Access settings") { shell.openFullDiskAccess() }
            Button("Relaunch to apply") { shell.relaunch() }
        }

        if shell.stagedBytes > 0 {
            Divider()
            Text("Staging: \(humanBytes(shell.stagedBytes))")
            Button("Show in Finder") { shell.showStaging() }
        }

        if let reason = shell.environmentFailure {
            Divider()
            Text("Login environment not captured: \(reason)")
        }

        Divider()
        Button("Scan…") { shell.openScan() }
        Button("Quit regrow") { MainRunLoop.quit() }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var signals: [DispatchSourceSignal] = []

    func applicationDidFinishLaunching(_ notification: Notification) {
        // A signal source only sees a signal whose default action is
        // off. Children get every default back when they are started.
        for number in [SIGTERM, SIGINT] {
            signal(number, SIG_IGN)
            let source = DispatchSource.makeSignalSource(signal: number, queue: .main)
            source.setEventHandler { MainRunLoop.quit() }
            source.resume()
            signals.append(source)
        }
        NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: .main
        ) { _ in
            MainActor.assumeIsolated { Shell.shared.woke() }
        }
        Shell.shared.start()
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        Shell.shared.shutdown {
            NSApp.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }
}
