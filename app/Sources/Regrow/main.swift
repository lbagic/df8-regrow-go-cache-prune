import Foundation
import RegrowKit

// One executable, four ways to run it. The three with an argument show
// no menu and exit when done:
//
//   --swap <staged bundle> <destination>   put a bundle in place
//   --smoke <dir>                          trash a sentinel and undo it
//   --smoke-alert <dir>                    one tick, its notifications

private func usage(_ text: String) -> Never {
    FileHandle.standardError.write(Data("usage: Regrow \(text)\n".utf8))
    exit(64)
}

let arguments = Array(CommandLine.arguments.dropFirst())

switch arguments.first {
case "--swap":
    guard arguments.count == 3 else { usage("--swap <staged bundle> <destination>") }
    do {
        try BundleSwap.swap(
            staged: URL(fileURLWithPath: arguments[1]), destination: URL(fileURLWithPath: arguments[2]))
        exit(0)
    } catch {
        FileHandle.standardError.write(Data("regrow: \(error)\n".utf8))
        exit(1)
    }
case "--smoke":
    guard arguments.count == 2 else { usage("--smoke <dir>") }
    Smoke.run(.trash, directory: arguments[1])
case "--smoke-alert":
    guard arguments.count == 2 else { usage("--smoke-alert <dir>") }
    Smoke.run(.alert, directory: arguments[1])
default:
    RegrowApp.main()
}
