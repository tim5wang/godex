import UIKit
import Capacitor
import WebKit

// godex Mobile 注入子类：在目标页 documentStart 注入 token + 任务完成轮询 watcher。
// 与默认 CAPBridgeViewController 的差异仅在于 viewDidLoad 时追加一个 WKUserScript，
// 从 Bundle 读取 public/godex-watcher.js 模板并替换占位符。
final class GodexBridgeViewController: CAPBridgeViewController {

    override func viewDidLoad() {
        super.viewDidLoad()
        injectWatcherScript()
    }

    /// 从 UserDefaults（@capacitor/preferences 的 iOS 存储，前缀 CapacitorStorage.）
    /// 读取配置并注入 watcher。token 为空时仍注入（watcher 内部会跳过空 token）。
    private func injectWatcherScript() {
        guard let webView = self.webView else { return }

        let defaults = UserDefaults.standard
        let token = defaults.string(forKey: "CapacitorStorage.godex_token") ?? ""
        let pollMs = defaults.string(forKey: "CapacitorStorage.godex_poll_ms") ?? "30000"

        guard let templateURL = Bundle.main.url(forResource: "godex-watcher", withExtension: "js", subdirectory: "public"),
              var source = try? String(contentsOf: templateURL, encoding: .utf8) else {
            return
        }

        source = source
            .replacingOccurrences(of: "__GODEX_TOKEN__", with: escapeForJS(token))
            .replacingOccurrences(of: "__GODEX_POLL_MS__", with: escapeForJS(pollMs))

        let script = WKUserScript(source: source, injectionTime: .atDocumentStart, forMainFrameOnly: true)
        webView.configuration.userContentController.addUserScript(script)
    }

    /// 将任意字符串安全地嵌入 JS 双引号字符串字面量。
    private func escapeForJS(_ raw: String) -> String {
        raw
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
            .replacingOccurrences(of: "\n", with: "\\n")
            .replacingOccurrences(of: "\r", with: "\\r")
    }
}

class SceneDelegate: UIResponder, UIWindowSceneDelegate {
    var window: UIWindow?

    func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options connectionOptions: UIScene.ConnectionOptions) {
        guard let windowScene = scene as? UIWindowScene else { return }

        window = UIWindow(windowScene: windowScene)
        window?.rootViewController = GodexBridgeViewController()
        window?.makeKeyAndVisible()

        SceneDelegateProxy.shared.scene(scene, willConnectTo: session, options: connectionOptions)
    }

    func scene(_ scene: UIScene, openURLContexts URLContexts: Set<UIOpenURLContext>) {
        SceneDelegateProxy.shared.scene(scene, openURLContexts: URLContexts)
    }

    func scene(_ scene: UIScene, continue userActivity: NSUserActivity) {
        SceneDelegateProxy.shared.scene(scene, continue: userActivity)
    }
}
