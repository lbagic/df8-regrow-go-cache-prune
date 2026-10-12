import Foundation
import RegrowKit

// One executable, four ways to run it. The three with an argument show
// no menu and exit when done:
//
//   --swap ~/Applications/.Regrow.app.staged ~/Applications/Regrow.app
//                                          put the staged bundle in place
//   --smoke <dir>                          trash a sentinel and undo it
//   --smoke-alert <dir>                    one tick, its notifications
//
// Anything can start the app with these arguments, and a sealed bundle
// runs with the owner's grants: none of them names a path the app acts
// on beyond those shapes (`BundleSwap.check`, `SmokeRun.directory`).

private func usage(_ text: String) -> Never {
    FileHandle.standardError.write(Data("usage: Regrow \(text)\n".utf8))
    exit(64)
}

let arguments = Array(CommandLine.arguments.dropFirst())

switch arguments.first {
case "--swap":
    guard arguments.count == 3 else { usage("--swap ~/Applications/.Regrow.app.staged ~/Applications/Regrow.app") }
    do {
        let (staged, destination) = try BundleSwap.check(
            staged: URL(fileURLWithPath: arguments[1]), destination: URL(fileURLWithPath: arguments[2]),
            running: Bundle.main.bundleURL)
        try BundleSwap.swap(staged: staged, destination: destination)
        exit(0)
    } catch {
        FileHandle.standardError.write(Data("regrow: \(error)\n".utf8))
        exit(1)
    }
case "--smoke", "--smoke-alert":
    let mode: Smoke.Mode = arguments[0] == "--smoke" ? .trash : .alert
    guard arguments.count == 2 else { usage("\(arguments[0]) <dir>") }
    guard let directory = SmokeRun.directory(arguments[1], sealed: CodeSignature.runningSealed) else {
        usage("\(arguments[0]) <dir>, where <dir> is $(getconf DARWIN_USER_TEMP_DIR)regrow-smoke.<letters and digits>")
    }
    Smoke.run(mode, directory: directory)
default:
    RegrowApp.main()
}
