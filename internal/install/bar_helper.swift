// MiBee NVR macOS 菜单栏助手 — install 时由 swiftc 现场编译。
// 模板占位符 __BASE_URL__ 由 internal/install 注入（http://127.0.0.1:<port>）。
// 纯 AppKit、.accessory 策略（只占菜单栏，不进 Dock）。

import AppKit

let baseURL = URL(string: "__BASE_URL__")!

final class Delegate: NSObject, NSApplicationDelegate {
    let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)

    func applicationDidFinishLaunching(_ note: Notification) {
        if let img = NSImage(systemSymbolName: "video.fill", accessibilityDescription: "MiBee NVR") {
            item.button?.image = img
        } else {
            item.button?.image = NSImage(named: NSImage.networkTemplateName)
        }

        let menu = NSMenu()
        menu.addItem(withTitle: "打开 Web 界面", action: #selector(openWeb(_:)), keyEquivalent: "w")
        menu.addItem(withTitle: "修改密码…", action: #selector(changePassword(_:)), keyEquivalent: "p")
        menu.addItem(.separator())
        menu.addItem(withTitle: "退出 MiBee NVR", action: #selector(shutdownNVR(_:)), keyEquivalent: "q")
        for mi in menu.items { mi.target = self }
        item.menu = menu
    }

    @objc func openWeb(_ sender: Any?) {
        NSWorkspace.shared.open(baseURL)
    }

    @objc func changePassword(_ sender: Any?) {
        let alert = NSAlert()
        alert.messageText = "修改 MiBee NVR 管理密码"
        alert.informativeText = "密码用于局域网/远程登录（本机浏览器免密）。至少 8 个字符。"

        let f1 = NSSecureTextField(frame: NSRect(x: 0, y: 0, width: 240, height: 24))
        f1.placeholderString = "新密码"
        let f2 = NSSecureTextField(frame: NSRect(x: 0, y: 0, width: 240, height: 24))
        f2.placeholderString = "确认新密码"
        let stack = NSStackView(views: [f1, f2])
        stack.orientation = .vertical
        stack.spacing = 8
        alert.accessoryView = stack
        alert.addButton(withTitle: "确定")
        alert.addButton(withTitle: "取消")

        guard alert.runModal() == .alertFirstButtonReturn else { return }
        let pw = f1.stringValue
        guard pw.count >= 8 else {
            self.notify("新密码至少 8 个字符。", style: .warning); return
        }
        guard pw == f2.stringValue else {
            self.notify("两次输入的新密码不一致。", style: .warning); return
        }

        var req = URLRequest(url: baseURL.appendingPathComponent("api/auth/password"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: ["new_password": pw])
        let ok = postSync(req)
        if ok {
            self.notify("密码已修改（局域网登录请使用新密码）。", style: .informational)
        } else {
            self.notify("修改失败——NVR 未运行或尚未完成初始设置。", style: .warning)
        }
    }

    @objc func shutdownNVR(_ sender: Any?) {
        var req = URLRequest(url: baseURL.appendingPathComponent("api/system/shutdown"))
        req.httpMethod = "POST"
        _ = postSync(req)
        // 助手自身也随之退出：NVR 已停，菜单栏不应再挂着操作入口。
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) { exit(0) }
    }

    private func postSync(_ req: URLRequest) -> Bool {
        var ok = false
        let sem = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: req) { _, resp, _ in
            if let http = resp as? HTTPURLResponse { ok = (200..<300).contains(http.statusCode) }
            sem.signal()
        }.resume()
        _ = sem.wait(timeout: .now() + 5)
        return ok
    }

    private func notify(_ text: String, style: NSAlert.Style) {
        let a = NSAlert()
        a.alertStyle = style
        a.messageText = text
        a.runModal()
    }
}

let app = NSApplication.shared
let delegate = Delegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
