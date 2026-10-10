import AppKit
import RegrowKit
import WebKit

/// The scan window: the page in a web view that can load that one file
/// and go nowhere else, and the bridge between the page and the shell.
@MainActor
final class ScanWindow: NSObject, NSWindowDelegate, WKNavigationDelegate, WKScriptMessageHandler {
    /// The name the page posts to: `window.webkit.messageHandlers.regrow`.
    private static let handler = "regrow"

    private let window: KeyWindow
    private let webView: WKWebView
    private let page: URL
    private let log: LogFile
    private let onMessage: (Any) -> Void
    private let onPageGone: () -> Void
    private let onClose: () -> Void

    private var firstLoadAllowed = false
    private var lastReload = Date.distantPast

    /// `onPageGone` is called when the page's requests have nobody left
    /// to answer: the window closed, or the page's process ended.
    init(
        page: URL, log: LogFile,
        onMessage: @escaping (Any) -> Void,
        onPageGone: @escaping () -> Void,
        onClose: @escaping () -> Void
    ) {
        self.page = page
        self.log = log
        self.onMessage = onMessage
        self.onPageGone = onPageGone
        self.onClose = onClose

        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        configuration.preferences.javaScriptCanOpenWindowsAutomatically = false
        let frame = NSRect(x: 0, y: 0, width: 900, height: 640)
        webView = WKWebView(frame: frame, configuration: configuration)
        window = KeyWindow(
            contentRect: frame, styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered, defer: false)
        super.init()

        // No UI delegate: without one the page cannot open a window or
        // a panel.
        webView.navigationDelegate = self
        webView.configuration.userContentController.add(self, name: Self.handler)
        window.title = "regrow"
        window.contentMinSize = NSSize(width: 720, height: 520)
        window.isReleasedWhenClosed = false
        window.delegate = self
        window.contentView = webView
        window.center()
        load()
    }

    func show() {
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// Hands one event line to the page. The line is an argument of the
    /// call, never part of its source. A line for a page that has not
    /// set itself up yet is dropped: the page asks for what stands when
    /// it says it is ready.
    func deliver(_ line: String) {
        // The call returns a value because one that returns none is
        // reported as a failure.
        webView.callAsyncJavaScript(
            "if (window.regrow && window.regrow.receive) { window.regrow.receive(line); return true } return false",
            arguments: ["line": line], in: nil, in: .page
        ) { [log] result in
            if case .failure(let error) = result {
                log("the page could not take an event: \(error.localizedDescription)")
            }
        }
    }

    private func load() {
        firstLoadAllowed = false
        webView.loadFileURL(page, allowingReadAccessTo: page)
    }

    // MARK: WKNavigationDelegate

    func webView(
        _ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
        decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
    ) {
        let url = navigationAction.request.url
        let isPage = url.map { $0.isFileURL && $0.resolvingSymlinksInPath().path == page.resolvingSymlinksInPath().path } ?? false
        if !firstLoadAllowed, isPage, navigationAction.targetFrame?.isMainFrame == true {
            firstLoadAllowed = true
            decisionHandler(.allow)
        } else {
            log("navigation denied: \(url?.absoluteString ?? "no URL")")
            decisionHandler(.cancel)
        }
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        log("the page did not load from \(page.path): \(error.localizedDescription)")
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        onPageGone()
        // A page that ends its process as it loads would load for ever.
        guard Date().timeIntervalSince(lastReload) > 10 else {
            log("the page's process ended again within 10 s; close the window and open it again to retry")
            return
        }
        log("the page's process ended; loading the page again")
        lastReload = Date()
        load()
    }

    // MARK: WKScriptMessageHandler

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame else { return }
        onMessage(message.body)
    }

    // MARK: NSWindowDelegate

    func windowWillClose(_ notification: Notification) {
        // The content controller holds its handler strongly.
        webView.configuration.userContentController.removeScriptMessageHandler(forName: Self.handler)
        webView.navigationDelegate = nil
        onPageGone()
        onClose()
    }
}

/// An app that lives in the menu bar has no main menu to carry the
/// usual key equivalents, so the window answers them itself.
private final class KeyWindow: NSWindow {
    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if super.performKeyEquivalent(with: event) {
            return true
        }
        guard event.type == .keyDown,
            event.modifierFlags.intersection(.deviceIndependentFlagsMask) == .command
        else { return false }
        switch event.charactersIgnoringModifiers {
        case "w":
            performClose(nil)
            return true
        case "c": return NSApp.sendAction(#selector(NSText.copy(_:)), to: nil, from: self)
        case "x": return NSApp.sendAction(#selector(NSText.cut(_:)), to: nil, from: self)
        case "v": return NSApp.sendAction(#selector(NSText.paste(_:)), to: nil, from: self)
        case "a": return NSApp.sendAction(#selector(NSText.selectAll(_:)), to: nil, from: self)
        default: return false
        }
    }
}
