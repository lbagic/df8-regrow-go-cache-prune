import Foundation
import RegrowKit
import UserNotifications

/// Posts notices through the notification centre.
final class Notifier: NSObject, UNUserNotificationCenterDelegate, @unchecked Sendable {
    private let log: LogFile
    /// The centre raises an exception in a process that has no bundle,
    /// so it is nil for a bare executable.
    private let center: UNUserNotificationCenter?

    init(log: LogFile) {
        self.log = log
        center = Bundle.main.bundleIdentifier == nil ? nil : .current()
        super.init()
        center?.delegate = self
    }

    /// Asks the owner once; macOS remembers the answer. `answer` is
    /// called off the main thread.
    func askPermission(_ answer: ((Bool) -> Void)? = nil) {
        guard let center else {
            log("no bundle, so no notification permission to ask for")
            answer?(false)
            return
        }
        center.requestAuthorization(options: [.alert, .sound]) { [log] granted, error in
            let reason = error.map { ": \($0.localizedDescription)" } ?? ""
            log("notification permission \(granted ? "granted" : "not granted")\(reason)")
            answer?(granted)
        }
    }

    /// Whether a notice would be shown, and the centre's word for why
    /// not. `answer` is called off the main thread.
    func permission(_ answer: @escaping (_ allowed: Bool?, _ status: String) -> Void) {
        guard let center else {
            answer(false, "not in an app bundle")
            return
        }
        center.getNotificationSettings { settings in
            switch settings.authorizationStatus {
            case .authorized, .provisional: answer(true, "allowed")
            case .denied: answer(false, "denied in System Settings")
            case .notDetermined: answer(nil, "never asked")
            default: answer(false, "unknown status")
            }
        }
    }

    /// `taken` says whether the centre accepted the notice; it is
    /// called off the main thread.
    func post(_ notice: Notice, taken: ((Bool) -> Void)? = nil) {
        guard let center else {
            log("no bundle, so no notification: \(notice.title)")
            taken?(false)
            return
        }
        let content = UNMutableNotificationContent()
        content.title = notice.title
        content.body = notice.body
        content.sound = .default
        let request = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        center.add(request) { [log] error in
            if let error {
                log("notification not posted (\(notice.title)): \(error.localizedDescription)")
            } else {
                log("notification posted: \(notice.title)")
            }
            taken?(error == nil)
        }
    }

    /// Without this a notice is not shown while the scan window is in
    /// front.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter, willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .list, .sound])
    }
}
