// KroosBar: menu bar status for the kroos launchd agent.
// Build: swiftc -O KroosBar.swift -o kroosbar   Usage: kroosbar <repo root>
import AppKit

let root = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : NSHomeDirectory() + "/kroos-prod"
let logs = NSHomeDirectory() + "/Library/Logs/kroos"
let gui = "gui/\(getuid())"

@discardableResult
func sh(_ path: String, _ args: [String]) -> String {
    let p = Process()
    p.executableURL = URL(fileURLWithPath: path)
    p.arguments = args
    let out = Pipe()
    p.standardOutput = out
    p.standardError = FileHandle.nullDevice
    guard (try? p.run()) != nil else { return "" }
    let data = out.fileHandleForReading.readDataToEndOfFile()
    p.waitUntilExit()
    return String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
}

final class App: NSObject, NSApplicationDelegate, NSMenuDelegate {
    let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    let menu = NSMenu()

    func applicationDidFinishLaunching(_ n: Notification) {
        menu.delegate = self
        item.menu = menu
        refresh()
        Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in self?.refresh() }
    }

    func pid() -> String { sh("/usr/bin/pgrep", ["-x", "kroos"]).split(separator: "\n").first.map(String.init) ?? "" }

    func refresh() {
        let up = !pid().isEmpty
        item.button?.title = "🤖"
        item.button?.alphaValue = up ? 1.0 : 0.3  // dimmed when kroos is down
    }

    // Rebuild details each time the menu opens.
    func menuNeedsUpdate(_ m: NSMenu) {
        refresh()
        m.removeAllItems()
        let p = pid()
        if p.isEmpty {
            info("Not running")
        } else {
            info("Running (pid \(p), up \(sh("/bin/ps", ["-o", "etime=", "-p", p])))")
        }
        info("Deployed: " + String(sh("/usr/bin/git", ["-C", root, "log", "-1", "--format=%h %s (%cr)"]).prefix(60)))
        if let log = try? String(contentsOfFile: logs + "/deploy.log", encoding: .utf8),
           let last = log.split(separator: "\n").last(where: { $0.contains("deployed") || $0.contains("failed") }) {
            info("Last deploy: \(last)")
        }
        m.addItem(.separator())
        action("Restart", #selector(restart))
        action("Deploy now", #selector(deploy))
        action("Agent log", #selector(agentLog))
        action("Deploy log", #selector(deployLog))
        m.addItem(.separator())
        action("Quit", #selector(NSApplication.terminate(_:)), target: NSApp)
    }

    func info(_ s: String) { menu.addItem(NSMenuItem(title: s, action: nil, keyEquivalent: "")) }
    func action(_ t: String, _ sel: Selector, target: AnyObject? = nil) {
        let i = NSMenuItem(title: t, action: sel, keyEquivalent: "")
        i.target = target ?? self
        menu.addItem(i)
    }

    @objc func restart() { sh("/bin/launchctl", ["kickstart", "-k", "\(gui)/com.kroos.agent"]); after() }
    @objc func deploy() { sh("/bin/launchctl", ["kickstart", "\(gui)/com.kroos.deploy"]) }
    @objc func agentLog() { sh("/usr/bin/open", ["-a", "Console", logs + "/agent.log"]) }
    @objc func deployLog() { sh("/usr/bin/open", ["-a", "Console", logs + "/deploy.log"]) }
    func after() { DispatchQueue.main.asyncAfter(deadline: .now() + 2) { self.refresh() } }
}

let app = NSApplication.shared
let delegate = App()
app.delegate = delegate
app.setActivationPolicy(.accessory)
app.run()
