import AppKit

/// Work for the main thread that must not pass through the main
/// dispatch queue.
///
/// While AppKit waits for the answer to `terminateLater` it runs a
/// nested run loop. Entered from a block of the main dispatch queue,
/// that loop never drains the queue again: a quit started there would
/// wait for ever for an answer sent with `DispatchQueue.main.async`.
/// The run loop's own blocks run in either case.
enum MainRunLoop {
    static func perform(_ work: @escaping @MainActor () -> Void) {
        CFRunLoopPerformBlock(CFRunLoopGetMain(), CFRunLoopMode.commonModes.rawValue) {
            MainActor.assumeIsolated { work() }
        }
        CFRunLoopWakeUp(CFRunLoopGetMain())
    }

    /// Asks the app to quit, from outside any dispatch block.
    static func quit() {
        perform { NSApp.terminate(nil) }
    }
}
