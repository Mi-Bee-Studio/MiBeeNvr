// MiBee NVR macOS 菜单栏助手 — install 时由 swiftc 现场编译。
// 模板占位符 __BASE_URL__ 由 internal/install 注入（http://127.0.0.1:<port>）。
// 纯 AppKit、.accessory 策略（只占菜单栏，不进 Dock）。

import AppKit

let baseURL = URL(string: "__BASE_URL__")!

final class Delegate: NSObject, NSApplicationDelegate {
    let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)

    func applicationDidFinishLaunching(_ note: Notification) {
        // SF Symbols 兜底链：video.fill 主选、video 次选；连 SF Symbols 都没有
        // 的老系统退回纯文字。不用 NSImage.networkTemplateName——新 SDK 已移除
        // 该常量，swiftc 现场编译会直接失败（菜单栏助手整个装不上）。
        if let img = NSImage(systemSymbolName: "video.fill", accessibilityDescription: "MiBee NVR")
            ?? NSImage(systemSymbolName: "video", accessibilityDescription: "MiBee NVR") {
            item.button?.image = img
        } else {
            item.button?.title = "MiBee"
        }

        let menu = NSMenu()
        menu.addItem(withTitle: "打开 Web 界面", action: #selector(openWeb(_:)), keyEquivalent: "w")
        menu.addItem(withTitle: "修改密码…", action: #selector(changePassword(_:)), keyEquivalent: "p")
        menu.addItem(withTitle: "监听地址…", action: #selector(changeListen(_:)), keyEquivalent: "l")
        menu.addItem(.separator())
        menu.addItem(withTitle: "退出 MiBee NVR", action: #selector(shutdownNVR(_:)), keyEquivalent: "q")
        for mi in menu.items { mi.target = self }
        item.menu = menu
    }

    // 菜单 action 在 event-tracking runloop 模式里同步触发；在那里直接
    // runModal 弹窗不可见，且可能未经用户输入就返回「确定」（2026-09-20
    // 现场：点「卸载」没有任何弹窗，卸载却在后台执行了；「修改密码」
    // 同样无弹窗即静默失败）。统一做法：跳到下一个 runloop tick（脱离
    // 菜单追踪模式）并先激活本应用再弹窗——状态栏应用的标配写法。
    private func presentOnMain(_ body: @escaping () -> Void) {
        DispatchQueue.main.async {
            NSApp.activate(ignoringOtherApps: true)
            body()
        }
    }

    @objc func openWeb(_ sender: Any?) {
        NSWorkspace.shared.open(baseURL)
    }

    @objc func changePassword(_ sender: Any?) {
        presentOnMain { self.changePasswordDialog() }
    }

    private func changePasswordDialog() {
        let alert = NSAlert()
        alert.messageText = "修改 MiBee NVR 管理密码"
        alert.informativeText = "密码用于局域网/远程登录（本机浏览器免密）。至少 8 个字符。"

        let f1 = NSSecureTextField(frame: NSRect(x: 0, y: 32, width: 260, height: 24))
        f1.placeholderString = "新密码"
        let f2 = NSSecureTextField(frame: NSRect(x: 0, y: 0, width: 260, height: 24))
        f2.placeholderString = "确认新密码"
        // NSAlert 按 accessoryView 的 frame 定弹窗尺寸；NSStackView 走
        // 自动布局在部分系统上拿不到正确 fittingSize，弹窗会被压扁导致
        // 输入框显示不全（现场 2026-09-20）。显式 frame 的容器最稳。
        let box = NSView(frame: NSRect(x: 0, y: 0, width: 260, height: 60))
        box.addSubview(f1)
        box.addSubview(f2)
        alert.accessoryView = box
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

    @objc func changeListen(_ sender: Any?) {
        presentOnMain { self.changeListenDialog() }
    }

    private func changeListenDialog() {
        let alert = NSAlert()
        alert.messageText = "修改监听地址"
        alert.informativeText = "默认仅本机可访问（127.0.0.1）。填 0.0.0.0:9090 可开放局域网访问。"

        let f = NSTextField(frame: NSRect(x: 0, y: 0, width: 240, height: 24))
        var cur = "127.0.0.1:9090"
        if let c = URLComponents(url: baseURL, resolvingAgainstBaseURL: false), let h = c.host {
            cur = h
            if let p = c.port { cur += ":\(p)" }
        }
        f.stringValue = cur
        alert.accessoryView = f
        alert.addButton(withTitle: "确定")
        alert.addButton(withTitle: "取消")

        guard alert.runModal() == .alertFirstButtonReturn, !f.stringValue.isEmpty else { return }
        var req = URLRequest(url: baseURL.appendingPathComponent("api/system/listen"))
        req.httpMethod = "PUT"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: ["listen": f.stringValue])
        // 202 = 已受理：NVR 随即换绑并自动重编译/重启本助手（新地址生效），
        // 弹窗可能被助手重启打断——那本身就是切换成功的信号。
        _ = postSync(req)
    }

    @objc func shutdownNVR(_ sender: Any?) {
        var req = URLRequest(url: baseURL.appendingPathComponent("api/system/shutdown"))
        req.httpMethod = "POST"
        _ = postSync(req)
        // 服务端停机时已 bootout 双 agent（KeepAlive 不会再拉起 NVR）；
        // 助手也要把自身的 agent 注销，否则 launchd 会立刻让图标复活
        // （现场 2026-09-20：退出一次图标又出现，要退两次）。
        let uid = getuid()
        if let p = try? Process.run(URL(fileURLWithPath: "/bin/launchctl"),
                                    arguments: ["bootout", "gui/\(uid)/com.mibee-nvr.bar"]) {
            _ = p
        }
        exit(0)
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
        NSApp.activate(ignoringOtherApps: true)
        a.runModal()
    }
}

let app = NSApplication.shared
let delegate = Delegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
