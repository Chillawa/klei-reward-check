import AppKit
import WebKit

private let accountURL = URL(string: "https://accounts.klei.com/account/info")!

private func officialHost(_ url: URL?) -> Bool {
    guard let url = url else { return false }
    return url.scheme?.lowercased() == "https" && url.host?.lowercased() == "accounts.klei.com"
        && (url.port == nil || url.port == 443) && url.user == nil && url.password == nil
}

private func officialAccount(_ url: URL?) -> Bool {
    return officialHost(url) && (url?.path == "/account/info" || url?.path == "/account/info/")
}

private let renderedAccount = #"""
(() => {
    if (location.origin !== 'https://accounts.klei.com' || !/^\/account\/info\/?$/.test(location.pathname))
        return false;
    const id = /\bKU_[A-Za-z0-9_-]{6,32}\b/;
    function visible(element) {
        if (!element || !element.getClientRects().length) return false;
        if (getComputedStyle(element).visibility !== 'visible') return false;
        for (let node = element; node; node = node.parentElement) {
            if (getComputedStyle(node).opacity === '0') return false;
        }
        return true;
    }
    const body = document.body;
    if (!visible(body)) return false;
    if (id.test(body.innerText)) {
        const text = document.createTreeWalker(body, NodeFilter.SHOW_TEXT);
        while (text.nextNode()) {
            if (id.test(text.currentNode.data) && visible(text.currentNode.parentElement)) return true;
        }
    }
    return [...body.querySelectorAll('input[readonly]')].some(field => visible(field) && id.test(field.value));
})()
"""#

private func cookieRecord(_ cookie: HTTPCookie) -> [String: Any]? {
    guard ["accounts.klei.com", ".accounts.klei.com", ".klei.com"].contains(cookie.domain.lowercased()),
          cookie.path.hasPrefix("/") else { return nil }
    let prefix = cookie.path.hasSuffix("/") ? cookie.path : cookie.path + "/"
    guard ["/account/info", "/link/"].contains(where: { $0 == cookie.path || $0.hasPrefix(prefix) })
    else { return nil }
    return ["domain": cookie.domain, "path": cookie.path, "name": cookie.name, "value": cookie.value,
            "expires": cookie.expiresDate?.timeIntervalSince1970 ?? -1, "httpOnly": cookie.isHTTPOnly,
            "secure": cookie.isSecure]
}

private func writeError(_ text: String) {
    FileHandle.standardError.write(Data((PanelLanguage.message(text) + "\n").utf8))
}

private func finishJSON(_ object: [String: Any]) -> Never {
    guard var data = try? JSONSerialization.data(withJSONObject: object) else {
        writeError("无法读取本次登录结果。")
        exit(1)
    }
    data.append(0x0a)
    FileHandle.standardOutput.write(data)
    exit(0)
}

private enum LoginMode { case interactive, fixture, probe }

private let renderedPage = #"""
(() => {
    const body = document.body;
    if (!body) return false;
    function painted(element) {
        if (!element || getComputedStyle(element).visibility !== 'visible') return false;
        for (let node = element; node; node = node.parentElement)
            if (Number(getComputedStyle(node).opacity) === 0) return false;
        return element.getClientRects().length > 0;
    }
    if (location.origin === 'https://accounts.klei.com' && /^\/login\/?$/.test(location.pathname)) {
        const steam = body.querySelector('a[href="/login/steam"]');
        if (!painted(steam)) return false;
        const r = steam.getBoundingClientRect();
        return r.width > 0 && r.height > 0;
    }
    return painted(body) && (body.innerText.trim().length > 0 ||
        [...body.querySelectorAll('input, button, iframe, img, canvas')].some(painted));
})()
"""#

// Only the anonymous probes inspect the Steam control's pixels. Never snapshot a user session.
private let steamButtonRect = #"""
(() => {
    if (location.origin !== 'https://accounts.klei.com' || !/^\/login\/?$/.test(location.pathname)) return null;
    const link = document.querySelector('a[href="/login/steam"]');
    if (!link) return null;
    link.scrollIntoView({block:'nearest'});
    const r = link.getBoundingClientRect();
    const x = Math.max(0,r.left), y = Math.max(0,r.top);
    const w = Math.min(innerWidth,r.right)-x, h = Math.min(innerHeight,r.bottom)-y;
    if (w < 4 || h < 4 || !link.contains(document.elementFromPoint(x+w/2,y+h/2))) return null;
    return [x,y,w,h];
})()
"""#

private final class LoginBackground: NSView {
    override func draw(_ dirtyRect: NSRect) {
        NSColor.windowBackgroundColor.setFill()
        dirtyRect.fill()
    }
}

private final class LoginWindow: NSObject, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate {
    private let mode: LoginMode
    private let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 940, height: 700),
                                  styleMask: [.titled, .closable, .miniaturizable, .resizable],
                                  backing: .buffered, defer: false)
    private let pages = NSView()
    private let overlay = LoginBackground()
    private let status = NSTextField(labelWithString: "")
    private let spinner = NSProgressIndicator()
    private let retry = NSButton(title: PanelLanguage.text("重试"), target: nil, action: nil)
    private var tabs = [WKWebView]()
    private var route: KleiLoginRoute?
    private var navigationVersion = 0
    private var committed = false
    private var waitingSince: TimeInterval?
    private var checkingRender = false
    private var checking = false
    private var fixtureAttempt = 0
    private var hiddenFixture = false
    private var probingButton = false

    init(mode: LoginMode) {
        self.mode = mode
        super.init()
    }

    func start() {
        window.delegate = self
        window.minSize = NSSize(width: 520, height: 400)
        window.center()
        let content = NSView()
        window.contentView = content
        for view in [pages, overlay] {
            content.addSubview(view)
            view.translatesAutoresizingMaskIntoConstraints = false
            NSLayoutConstraint.activate([
                view.leadingAnchor.constraint(equalTo: content.leadingAnchor), view.trailingAnchor.constraint(equalTo: content.trailingAnchor),
                view.topAnchor.constraint(equalTo: content.topAnchor), view.bottomAnchor.constraint(equalTo: content.bottomAnchor)
            ])
        }
        spinner.style = .spinning
        spinner.controlSize = .small
        retry.target = self
        retry.action = #selector(retryPage)
        let message = NSStackView(views: [spinner, status, retry])
        message.orientation = .vertical
        message.spacing = 10
        overlay.addSubview(message)
        message.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([message.centerXAnchor.constraint(equalTo: overlay.centerXAnchor),
                                     message.centerYAnchor.constraint(equalTo: overlay.centerYAnchor)])
        content.layoutSubtreeIfNeeded()
        beginNavigation()
        if mode == .interactive {
            addEditMenu()
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            writeError("LOGIN_WINDOW_READY")
        }
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.allowsAirPlayForMediaPlayback = false
        if mode == .fixture {
            openAccount(in: addPage(config))
        } else {
            KleiLoginRoute.prepare(for: config.websiteDataStore) { route in
                self.route = route
                self.openAccount(in: self.addPage(config))
            }
        }
        Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
            self?.inspectPage()
        }
        Timer.scheduledTimer(withTimeInterval: mode == .interactive ? 1800 : 70, repeats: false) { _ in
            writeError("等待登录页面超时。")
            exit(1)
        }
    }

    private func addEditMenu() {
        let menu = NSMenu()
        let appMenu = NSMenu()
        let appItem = NSMenuItem()
        menu.addItem(appItem)
        appItem.submenu = appMenu
        appMenu.addItem(withTitle: PanelLanguage.text("关闭登录窗口"), action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        let edit = NSMenu(title: PanelLanguage.text("编辑"))
        let editItem = NSMenuItem()
        menu.addItem(editItem)
        editItem.submenu = edit
        for (title, action, key) in [("剪切", "cut:", "x"), ("复制", "copy:", "c"),
                                     ("粘贴", "paste:", "v"), ("全选", "selectAll:", "a")] {
            edit.addItem(withTitle: PanelLanguage.text(title), action: Selector(action), keyEquivalent: key)
        }
        appMenu.addItem(withTitle: PanelLanguage.text("返回"), action: #selector(goBack), keyEquivalent: "[").target = self
        NSApp.mainMenu = menu
    }

    private func addPage(_ config: WKWebViewConfiguration) -> WKWebView {
        let view = WKWebView(frame: pages.bounds, configuration: config)
        view.autoresizingMask = [.width, .height]
        view.navigationDelegate = self
        view.uiDelegate = self
        tabs.last?.isHidden = true
        tabs.append(view)
        pages.addSubview(view)
        window.makeFirstResponder(view)
        beginNavigation()
        return view
    }

    private func updateTitle() {
        window.title = tabs.last?.url?.host ?? accountURL.host!
    }

    private func beginNavigation() {
        navigationVersion += 1
        committed = false
        checkingRender = false
        waitingSince = ProcessInfo.processInfo.systemUptime
        overlay.isHidden = false
        retry.isHidden = true
        spinner.isHidden = false
        spinner.startAnimation(nil)
        status.stringValue = PanelLanguage.text("正在打开登录页…")
        updateTitle()
    }

    private func showRetry(_ message: String) {
        overlay.isHidden = false
        spinner.stopAnimation(nil)
        spinner.isHidden = true
        retry.isHidden = false
        status.stringValue = PanelLanguage.message(message)
    }

    @objc private func goBack() {
        guard let view = tabs.last else { return }
        if view.canGoBack { view.goBack() }
        else if tabs.count > 1 { webViewDidClose(view) }
    }

    private func openAccount(in view: WKWebView) {
        if mode == .fixture {
            guard #available(macOS 12.0, *) else { exit(1) }
            fixtureAttempt += 1
            let login = URL(string: "https://accounts.klei.com/login")!
            let link = "<a href='/login/steam' style='display:inline-block;padding:15px;background:#246;color:white'>Steam</a>"
            let body = fixtureAttempt == 1 ? "<main hidden>\(link)</main>" : "<main>\(link)</main>"
            view.loadSimulatedRequest(URLRequest(url: login), responseHTML: body)
        } else {
            view.load(URLRequest(url: accountURL))
        }
    }

    @objc private func retryPage() {
        guard let view = tabs.last else { return }
        view.stopLoading()
        beginNavigation()
        openAccount(in: view)
    }

    func windowWillClose(_ notification: Notification) {
        writeError("登录窗口已关闭。")
        exit(2)
    }

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        // WebKit supplies the opener's ephemeral store and process configuration.
        guard mode == .interactive, navigationAction.targetFrame == nil else { return nil }
        return addPage(configuration)
    }

    func webViewDidClose(_ webView: WKWebView) {
        guard tabs.contains(where: { $0 === webView }) else { return }
        if webView === tabs.first { window.close(); return }
        tabs.removeAll { $0 === webView }
        webView.removeFromSuperview()
        tabs.last?.isHidden = false
        window.makeFirstResponder(tabs.last)
        beginNavigation()
        committed = true
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        let url = navigationAction.request.url
        let allowed = url?.scheme?.lowercased() == "https" || url?.absoluteString == "about:blank"
        decisionHandler(allowed && !navigationAction.shouldPerformDownload ? .allow : .cancel)
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationResponse: WKNavigationResponse,
                 decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
        if navigationResponse.isForMainFrame && !navigationResponse.canShowMIMEType {
            failed(webView, "登录网站返回了无法显示的内容。")
            decisionHandler(.cancel)
        } else if navigationResponse.isForMainFrame, let http = navigationResponse.response as? HTTPURLResponse,
                  http.statusCode >= 400 {
            failed(webView, "登录网站返回了错误（HTTP \(http.statusCode)）。")
            decisionHandler(.cancel)
        } else {
            decisionHandler(navigationResponse.canShowMIMEType ? .allow : .cancel)
        }
    }

    func webView(_ webView: WKWebView, didReceive challenge: URLAuthenticationChallenge,
                 completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        let trust = challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust
        completionHandler(trust ? .performDefaultHandling : .cancelAuthenticationChallenge, nil)
    }

    @available(macOS 12.0, *)
    func webView(_ webView: WKWebView, requestMediaCapturePermissionFor origin: WKSecurityOrigin,
                 initiatedByFrame frame: WKFrameInfo, type: WKMediaCaptureType,
                 decisionHandler: @escaping (WKPermissionDecision) -> Void) {
        decisionHandler(.deny)
    }

    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        completionHandler(nil)
    }

    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        if webView === tabs.last { beginNavigation() }
    }

    func webView(_ webView: WKWebView, didCommit navigation: WKNavigation!) {
        if webView === tabs.last {
            committed = true
            updateTitle()
        }
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        if officialHost(webView.url) && ["/account", "/account/"].contains(webView.url?.path) {
            webView.load(URLRequest(url: accountURL))
        } else if webView === tabs.last {
            updateTitle()
            inspectPage()
        }
    }

    private func failed(_ view: WKWebView, _ message: String) {
        guard view === tabs.last else { return }
        navigationVersion += 1
        waitingSince = nil
        checkingRender = false
        showRetry(message)
        if mode == .probe { writeError(message); exit(1) }
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        let cause = error as NSError
        if cause.domain == NSURLErrorDomain && cause.code == NSURLErrorCancelled { return }
        if cause.domain == WKError.errorDomain && cause.code == 102 { return } // navigation policy cancellation
        failed(webView, "无法连接登录网站。")
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        self.webView(webView, didFailProvisionalNavigation: navigation, withError: error)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        failed(webView, "登录页已中断。")
    }

    private func inspectPage() {
        guard let view = tabs.last else { return }
        guard let since = waitingSince else {
            if overlay.isHidden && mode != .probe { checkAccount() }
            return
        }
        let delayed = ProcessInfo.processInfo.systemUptime - since > (mode == .fixture ? 2 : 15)
        if delayed && retry.isHidden {
            showRetry(committed ? "登录页未能显示。" : "连接登录网站用时较长。")
        }
        guard committed, !checkingRender else { return }
        checkingRender = true
        let version = navigationVersion
        view.evaluateJavaScript(renderedPage) { value, _ in
            guard version == self.navigationVersion, view === self.tabs.last else { return }
            self.checkingRender = false
            guard value as? Bool == true else {
                if self.mode == .fixture && self.fixtureAttempt == 1 && delayed {
                    guard !self.overlay.isHidden, !self.retry.isHidden else { exit(1) }
                    self.retry.performClick(nil)
                }
                return
            }
            if self.mode == .fixture && self.fixtureAttempt == 1 {
                writeError("隐藏的登录按钮被误判为可见。")
                exit(1)
            }
            self.waitingSince = nil
            self.overlay.isHidden = true
            self.spinner.stopAnimation(nil)
            if self.mode != .interactive && !self.probingButton {
                self.probingButton = true
                self.verifySteamButton(in: view)
            }
        }
    }

    private func verifySteamButton(in view: WKWebView) {
        guard view.bounds.width > 400, view.bounds.height > 300, !view.isHidden, overlay.isHidden else {
            writeError("登录页显示区域过小或被遮挡。")
            exit(1)
        }
        view.evaluateJavaScript(steamButtonRect) { value, _ in
            guard let rect = value as? [CGFloat], rect.count == 4 else {
                writeError("网页上的 Steam 登录按钮不可点击。")
                exit(1)
            }
            let config = WKSnapshotConfiguration()
            config.rect = NSRect(x: rect[0], y: rect[1], width: rect[2], height: rect[3])
            config.afterScreenUpdates = false
            view.takeSnapshot(with: config) { image, _ in
                guard let data = image?.tiffRepresentation, let bitmap = NSBitmapImageRep(data: data),
                      let first = bitmap.colorAt(x: 0, y: 0)?.usingColorSpace(.deviceRGB) else {
                    writeError("无法检查登录按钮的实际画面。")
                    exit(1)
                }
                var painted = false
                for y in stride(from: 0, to: bitmap.pixelsHigh, by: 2) {
                    for x in stride(from: 0, to: bitmap.pixelsWide, by: 2) {
                        if let pixel = bitmap.colorAt(x: x, y: y)?.usingColorSpace(.deviceRGB),
                           abs(pixel.redComponent - first.redComponent) + abs(pixel.greenComponent - first.greenComponent)
                               + abs(pixel.blueComponent - first.blueComponent) > 0.12 {
                            painted = true
                            break
                        }
                    }
                    if painted { break }
                }
                guard painted else { writeError("Steam 登录按钮的画面为空白。"); exit(1) }
                if self.mode == .probe {
                    finishJSON(["ok": true, "host": "accounts.klei.com", "steam_button": true,
                                "rendered_pixels": true])
                }
                self.testRenderedAccount(view)
            }
        }
    }

    private func checkAccount() {
        guard !checking, let view = tabs.last, !view.isLoading, officialAccount(view.url) else { return }
        checking = true
        view.evaluateJavaScript(renderedAccount) { value, _ in
            if self.hiddenFixture {
                guard value as? Bool == false else { writeError("隐藏的账号 ID 检查未通过。"); exit(1) }
                self.hiddenFixture = false
                view.evaluateJavaScript("document.querySelector('input').hidden = false") { _, error in
                    guard error == nil else { exit(1) }
                    self.checking = false
                }
                return
            }
            guard value as? Bool == true, self.tabs.last === view, !view.isLoading, officialAccount(view.url) else {
                self.checking = false
                return
            }
            view.configuration.websiteDataStore.httpCookieStore.getAllCookies { cookies in
                guard self.tabs.last === view, !view.isLoading, officialAccount(view.url) else {
                    self.checking = false
                    return
                }
                let records = cookies.compactMap(cookieRecord)
                if self.mode == .fixture {
                    guard records.count == 1, records.first?["name"] as? String == "fixture" else {
                        writeError("离线 Cookie 筛选未通过。")
                        exit(1)
                    }
                    finishJSON(["ok": true])
                }
                guard !records.isEmpty else {
                    writeError("账户页面没有可用于独立会话的 Klei Cookie。")
                    exit(1)
                }
                finishJSON(["cookies": records])
            }
        }
    }

    private func testRenderedAccount(_ view: WKWebView) {
        guard #available(macOS 12.0, *) else { writeError("离线窗口测试需要 macOS 12 或更高版本。"); exit(1) }
        guard view.bounds.width > 400 && view.bounds.height > 300 else {
            writeError("登录网页显示区域过小。")
            exit(1)
        }
        hiddenFixture = true
        let store = view.configuration.websiteDataStore.httpCookieStore
        let sample = HTTPCookie(properties: [.domain: "accounts.klei.com", .path: "/", .name: "fixture", .value: "test"])!
        let other = HTTPCookie(properties: [.domain: "provider.invalid", .path: "/", .name: "provider", .value: "test"])!
        store.setCookie(other) {
            store.setCookie(sample) {
                let html = "<meta http-equiv='Content-Security-Policy' content=\"default-src 'none'\">"
                    + "<p>Klei</p><input readonly hidden value='KU_12345678'>"
                view.loadSimulatedRequest(URLRequest(url: accountURL), responseHTML: html)
            }
        }
    }
}

private let args = Array(CommandLine.arguments.dropFirst())
if args == ["--self-check"] {
    guard officialAccount(accountURL), !officialAccount(URL(string: "https://elsewhere.invalid/account/info")),
          !officialAccount(URL(string: "http://accounts.klei.com/account/info")),
          !officialAccount(URL(string: "https://accounts.klei.com:444/account/info")),
          !officialAccount(URL(string: "https://accounts.klei.com/login")) else { exit(1) }
    finishJSON(["ok": true])
}
private let mode: LoginMode
switch args {
case []: mode = .interactive
case ["--test-window"]: mode = .fixture
case ["--probe-login"]: mode = .probe
default: exit(64)
}
if mode == .interactive {
    var output = stat()
    guard fstat(STDOUT_FILENO, &output) == 0, [S_IFIFO, S_IFSOCK].contains(output.st_mode & S_IFMT) else {
        writeError("请从 Klei 点数自动领打开登录窗口。")
        exit(1)
    }
}
private let app = NSApplication.shared
PanelAppearance.restore()
app.setActivationPolicy(mode == .interactive ? .regular : .prohibited)
private let login = LoginWindow(mode: mode)
login.start()
app.run()
