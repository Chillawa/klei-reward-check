import AppKit
import Foundation

private let core = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/klei-core")
private let giftDescription = PanelLanguage.text("也领取 Klei 官方公告、通讯和活动图片中未标明种类的隐藏奖励。")

private struct Status: Decodable {
    let enabled: Bool
    let saved: Bool
    let include_gifts: Bool
    let login_needed: Bool
    let points: Int64?
    let points_at: Int64?
    let last: Int64?
    let notice: String?
    let receipts: [String]?
}

private func invoke(_ arguments: [String]) throws -> Data {
    let child = Process()
    let pipe = Pipe()
    child.executableURL = core
    child.arguments = arguments
    child.standardOutput = pipe
    child.standardError = pipe
    try child.run()
    let data = pipe.fileHandleForReading.readDataToEndOfFile()
    child.waitUntilExit()
    guard child.terminationStatus == 0 else {
        let message = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        throw NSError(domain: "Klei", code: 1, userInfo: [NSLocalizedDescriptionKey: message.isEmpty ? "操作失败，请重试。" : message])
    }
    return data
}

private final class BalanceCard: NSView {
    let refreshButton = NSButton(title: PanelLanguage.text("刷新"), target: nil, action: nil)
    private let total = NSTextField(labelWithString: "—")
    private let updated = NSTextField(labelWithString: PanelLanguage.text("正在读取余额…"))
    private let formatter = DateFormatter()

    override init(frame frameRect: NSRect) {
        super.init(frame: frameRect)
        let title = NSTextField(labelWithString: PanelLanguage.text("点数余额"))
        title.font = .systemFont(ofSize: 15, weight: .semibold)
        updated.font = .systemFont(ofSize: 12)
        updated.textColor = .secondaryLabelColor
        refreshButton.bezelStyle = .rounded
        refreshButton.toolTip = PanelLanguage.text("刷新余额")
        refreshButton.setAccessibilityLabel(PanelLanguage.text("刷新余额"))
        refreshButton.isHidden = true
        let coin = NSImageView()
        if let url = Bundle.main.url(forResource: "klei-points", withExtension: "png") {
            coin.image = NSImage(contentsOf: url)
        }
        coin.imageScaling = .scaleProportionallyUpOrDown
        coin.setAccessibilityElement(false)
        let heading = NSStackView(views: [title, NSView(), refreshButton])
        let amount = NSStackView(views: [coin, total])
        amount.alignment = .centerY
        amount.spacing = 9
        let content = NSStackView(views: [heading, amount, updated])
        content.orientation = .vertical
        content.alignment = .leading
        content.spacing = 5
        content.translatesAutoresizingMaskIntoConstraints = false
        addSubview(content)
        NSLayoutConstraint.activate([
            coin.widthAnchor.constraint(equalToConstant: 36),
            coin.heightAnchor.constraint(equalToConstant: 36),
            heading.widthAnchor.constraint(equalTo: widthAnchor),
            content.leadingAnchor.constraint(equalTo: leadingAnchor),
            content.trailingAnchor.constraint(equalTo: trailingAnchor),
            content.topAnchor.constraint(equalTo: topAnchor),
            content.bottomAnchor.constraint(equalTo: bottomAnchor)
        ])
        total.font = .monospacedDigitSystemFont(ofSize: 41, weight: .semibold)
        formatter.locale = PanelLanguage.locale
        formatter.setLocalizedDateFormatFromTemplate("yMdjm")
    }

    required init?(coder: NSCoder) { nil }

    func show(_ state: Status) {
        let points = state.saved ? state.points : nil
        total.stringValue = points?.formatted(.number.locale(PanelLanguage.locale)) ?? "—"
        let size: CGFloat = total.stringValue.count > 15 ? 21 : total.stringValue.count > 10 ? 30 : 41
        total.font = .monospacedDigitSystemFont(ofSize: size, weight: .semibold)
        if points != nil, let stamp = state.points_at ?? state.last, stamp > 0 {
            updated.stringValue = PanelLanguage.text("更新于 {0}", formatter.string(from: Date(timeIntervalSince1970: TimeInterval(stamp))))
        } else {
            updated.stringValue = PanelLanguage.text(state.login_needed ? "请重新登录以更新余额" : state.saved ? "等待读取余额" : "登录后可查看余额")
        }
        refreshButton.isHidden = !state.saved
        refreshButton.isEnabled = !state.login_needed
    }
}

private final class AppearanceButton: NSButton {
    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        updateImage()
    }

    func updateImage() {
        let dark = effectiveAppearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
        let label = PanelLanguage.text(dark ? "切换到浅色模式" : "切换到深色模式")
        image = NSImage(systemSymbolName: dark ? "moon.fill" : "moon", accessibilityDescription: label)
        toolTip = label
        setAccessibilityLabel(label)
    }
}

private final class Panel: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private let card = BalanceCard(frame: .zero)
    let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 460, height: 390),
                          styleMask: [.titled, .closable, .miniaturizable], backing: .buffered, defer: false)
    let button = NSButton(title: PanelLanguage.text("登录并开启"), target: nil, action: nil)
    let gifts = NSButton(checkboxWithTitle: PanelLanguage.text("同时领取神秘礼物"), target: nil, action: nil)
    private let giftHelp = NSButton(title: "", target: nil, action: nil)
    private let giftPopover = NSPopover()
    let info = NSTextField(wrappingLabelWithString: "")
    private let receipts = NSStackView()
    private var status: Status?
    private var busy = false
    private var refreshTimer: Timer?
    private var revision = 0
    private let stopButton = NSButton(title: PanelLanguage.text("停止"), target: nil, action: nil)
    private let appearanceButton = AppearanceButton(frame: NSRect(x: 0, y: 0, width: 34, height: 26))

    func applicationDidFinishLaunching(_ notification: Notification) {
        window.title = PanelLanguage.text("Klei 点数自动领")
        appearanceButton.isBordered = false
        appearanceButton.title = ""
        appearanceButton.imagePosition = .imageOnly
        appearanceButton.target = self
        appearanceButton.action = #selector(toggleAppearance)
        let accessory = NSTitlebarAccessoryViewController()
        accessory.layoutAttribute = .right
        accessory.view = appearanceButton
        window.addTitlebarAccessoryViewController(accessory)
        appearanceButton.updateImage()
        window.delegate = self
        window.isReleasedWhenClosed = false
        button.bezelStyle = .rounded
        button.target = self
        button.action = #selector(toggle)
        button.keyEquivalent = "\r"
        gifts.target = self
        gifts.action = #selector(changeGifts)
        gifts.setAccessibilityHelp(giftDescription)
        giftHelp.bezelStyle = .helpButton
        giftHelp.toolTip = giftDescription
        giftHelp.setAccessibilityLabel(PanelLanguage.text("神秘礼物说明"))
        giftHelp.setAccessibilityHelp(giftDescription)
        giftHelp.target = self
        giftHelp.action = #selector(showGiftHelp)
        card.refreshButton.target = self
        card.refreshButton.action = #selector(refreshBalance)
        let giftRow = NSStackView(views: [gifts, giftHelp])
        giftRow.orientation = .horizontal
        giftRow.alignment = .centerY
        giftRow.spacing = 6
        let section = NSTextField(labelWithString: PanelLanguage.text("自动领取"))
        let history = NSTextField(labelWithString: PanelLanguage.text("最近到账"))
        for title in [section, history] { title.font = .systemFont(ofSize: 15, weight: .semibold) }
        info.preferredMaxLayoutWidth = 412
        info.isHidden = true
        stopButton.target = self
        stopButton.action = #selector(stop)
        stopButton.isHidden = true
        let controls = NSStackView(views: [section, NSView(), button, stopButton])
        receipts.orientation = .vertical
        receipts.alignment = .leading
        receipts.spacing = 9
        let aboveAutomatic = NSBox()
        let aboveHistory = NSBox()
        for line in [aboveAutomatic, aboveHistory] { line.boxType = .separator }
        let stack = NSStackView(views: [card, info, aboveAutomatic, controls, giftRow, aboveHistory, history, receipts])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 12
        stack.setCustomSpacing(16, after: card)
        stack.setCustomSpacing(16, after: giftRow)
        stack.edgeInsets = NSEdgeInsets(top: 16, left: 24, bottom: 20, right: 24)
        for view in [card, controls, receipts, aboveAutomatic, aboveHistory] {
            view.widthAnchor.constraint(equalToConstant: 412).isActive = true
        }
        window.contentView = stack
        window.setContentSize(NSSize(width: 460, height: stack.fittingSize.height))
        window.center()
        if CommandLine.arguments.contains("--self-check") {
            guard let data = try? invoke(["--status"]),
                  let state = try? JSONDecoder().decode(Status.self, from: data) else { exit(1) }
            apply(state, message: nil)
            stack.layoutSubtreeIfNeeded()
            let controlsVisible = [button, gifts, giftHelp].allSatisfy { control in
                let rect = control.convert(control.bounds, to: stack)
                let minWidth: CGFloat = control === giftHelp ? 10 : 50
                return !control.isHidden && control.isEnabled && rect.width > minWidth && rect.height > 10 &&
                       stack.bounds.contains(NSPoint(x: rect.midX, y: rect.midY))
            }
            let valid = controlsVisible && button.action == #selector(toggle) && status != nil
            print(valid ? "ok" : "panel failed")
            exit(valid ? 0 : 1)
        }
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        button.isEnabled = false
        gifts.isEnabled = false
        // Disabling the loading controls can move AppKit's focus to the help button.
        window.makeFirstResponder(nil)
        refresh(nil, opening: true)
        refreshTimer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            if self?.busy == false { self?.refresh(nil) }
        }
    }
    private func apply(_ state: Status, message: String?) {
        status = state
        stopButton.isHidden = !(state.enabled && state.login_needed)
        stopButton.isEnabled = true
        busy = false
        gifts.state = state.include_gifts ? .on : .off
        button.title = PanelLanguage.text(state.login_needed ? "重新登录" : state.enabled ? "停止" : state.saved ? "开启" : "登录并开启")
        card.show(state)
        showReceipts(state.receipts ?? [])
        button.isEnabled = true
        gifts.isEnabled = true
        display(message ?? state.notice ?? "")
    }

    private func showReceipts(_ rows: [String]) {
        for view in receipts.arrangedSubviews { view.removeFromSuperview() }
        if rows.isEmpty {
            let empty = NSTextField(labelWithString: PanelLanguage.text("暂无到账记录"))
            empty.textColor = .secondaryLabelColor
            receipts.addArrangedSubview(empty)
            return
        }
        for row in rows {
            let fields = row.split(maxSplits: 1, whereSeparator: { $0.isWhitespace })
            guard fields.count == 2 else {
                receipts.addArrangedSubview(NSTextField(labelWithString: PanelLanguage.message(row)))
                continue
            }
            let date = NSTextField(labelWithString: String(fields[0]))
            date.textColor = .secondaryLabelColor
            date.font = .monospacedDigitSystemFont(ofSize: 13, weight: .regular)
            let amount = NSTextField(labelWithString: PanelLanguage.message(String(fields[1])))
            amount.font = .monospacedDigitSystemFont(ofSize: 13, weight: .semibold)
            let line = NSStackView(views: [date, NSView(), amount])
            receipts.addArrangedSubview(line)
            line.widthAnchor.constraint(equalTo: receipts.widthAnchor).isActive = true
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }
    private func display(_ message: String) {
        info.stringValue = PanelLanguage.message(message)
        info.isHidden = message.isEmpty
        if let stack = window.contentView as? NSStackView {
            stack.layoutSubtreeIfNeeded()
            let height = stack.fittingSize.height
            var frame = window.frame
            let newSize = window.frameRect(forContentRect: NSRect(x: 0, y: 0, width: 460, height: height)).size
            frame.origin.y += frame.height - newSize.height
            frame.size = newSize
            window.setFrame(frame, display: true)
        }
    }
    private func refresh(_ message: String?, opening: Bool = false) {
        let request = revision
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let state = try JSONDecoder().decode(Status.self, from: invoke([opening ? "--open-status" : "--status"]))
                DispatchQueue.main.async {
                    guard request == self.revision else { return }
                    self.apply(state, message: message)
                }
            } catch {
                DispatchQueue.main.async {
                    guard request == self.revision else { return }
                    self.busy = false
                    self.status = nil
                    self.gifts.isEnabled = false
                    self.stopButton.isEnabled = false
                    self.card.refreshButton.isEnabled = false
                    self.button.title = PanelLanguage.text("重试")
                    self.button.isEnabled = true
                    self.display(error.localizedDescription)
                }
            }
        }
    }
    @objc private func toggle() {
        guard let state = status else { refresh(nil, opening: true); return }
        change(state.login_needed ? "login" : state.enabled ? "stop" : "start")
    }
    @objc private func refreshBalance() { change("balance") }
    @objc private func toggleAppearance() {
        PanelAppearance.toggle(from: window.effectiveAppearance)
        appearanceButton.updateImage()
    }
    @objc private func stop() { change("stop") }
    @objc private func changeGifts() { change("gifts") }
    @objc private func showGiftHelp() {
        if giftPopover.isShown {
            giftPopover.performClose(nil)
            return
        }
        if giftPopover.contentViewController == nil {
            let label = NSTextField(wrappingLabelWithString: giftDescription)
            label.preferredMaxLayoutWidth = 240
            label.widthAnchor.constraint(equalToConstant: 240).isActive = true
            let content = NSStackView(views: [label])
            content.edgeInsets = NSEdgeInsets(top: 12, left: 14, bottom: 12, right: 14)
            let controller = NSViewController()
            controller.view = content
            giftPopover.contentViewController = controller
            giftPopover.contentSize = content.fittingSize
            giftPopover.behavior = .transient
        }
        giftPopover.show(relativeTo: giftHelp.bounds, of: giftHelp, preferredEdge: .maxY)
    }
    private func change(_ action: String) {
        guard !busy else { return }
        busy = true
        revision += 1
        stopButton.isEnabled = false
        card.refreshButton.isEnabled = false
        button.isEnabled = false
        gifts.isEnabled = false
        let value = gifts.state == .on ? "1" : "0"
        if action == "balance" { display("正在刷新余额…") }
        if action == "login" || (action == "start" && status?.saved == false) { display("正在打开登录窗口…") }
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                _ = try invoke(["--action", action, value])
                DispatchQueue.main.async { self.refresh(nil) }
            }
            catch {
                let message = error.localizedDescription
                DispatchQueue.main.async { self.refresh(message == "登录窗口已关闭。" ? nil : message) }
            }
        }
    }
}

@main
private enum RewardsApplication {
    static func main() {
        if ["--version", "--probe-login", "--test-window", "--status", "--open-status"].contains(CommandLine.arguments.dropFirst().first ?? "") {
            do { FileHandle.standardOutput.write(try invoke(Array(CommandLine.arguments.dropFirst()))) }
            catch { FileHandle.standardError.write(Data(PanelLanguage.message(error.localizedDescription).utf8)); exit(1) }
        } else {
            let app = NSApplication.shared
            PanelAppearance.restore()
            let test = CommandLine.arguments.contains("--self-check")
            app.setActivationPolicy(test ? .prohibited : .regular)
            let panel = Panel()
            app.delegate = panel
            app.run()
        }
    }
}
