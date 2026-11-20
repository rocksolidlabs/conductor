import Capacitor
import UIKit

// The shell's window: the shell's view controller in it, links the app is opened with handed to the native runtime (and so to the
// page), and a plain cover over everything while the app is not active, so the app switcher's snapshot never holds a card.
class SceneDelegate: UIResponder, UIWindowSceneDelegate {
    var window: UIWindow?
    private var cover: UIView?

    /** The page's own background: white in the light theme, ink in the dark. */
    static let coverColour = UIColor { traits in
        traits.userInterfaceStyle == .dark
            ? UIColor(red: 0x18 / 255.0, green: 0x21 / 255.0, blue: 0x1C / 255.0, alpha: 1)
            : UIColor.white
    }

    func scene(_ scene: UIScene, willConnectTo session: UISceneSession, options connectionOptions: UIScene.ConnectionOptions) {
        guard let windowScene = scene as? UIWindowScene else { return }
        window = UIWindow(windowScene: windowScene)
        window?.rootViewController = ShellViewController()
        window?.makeKeyAndVisible()
        SceneDelegateProxy.shared.scene(scene, willConnectTo: session, options: connectionOptions)
    }

    func scene(_ scene: UIScene, openURLContexts URLContexts: Set<UIOpenURLContext>) {
        SceneDelegateProxy.shared.scene(scene, openURLContexts: URLContexts)
    }

    func scene(_ scene: UIScene, continue userActivity: NSUserActivity) {
        SceneDelegateProxy.shared.scene(scene, continue: userActivity)
    }

    func sceneWillResignActive(_ scene: UIScene) {
        guard let window, cover == nil else { return }
        let v = UIView(frame: window.bounds)
        v.backgroundColor = Self.coverColour
        v.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        v.accessibilityElementsHidden = true
        window.addSubview(v)
        cover = v
    }

    func sceneDidBecomeActive(_ scene: UIScene) {
        cover?.removeFromSuperview()
        cover = nil
    }
}
