import SwiftUI
import UIKit

extension View {
    /// Keeps the edge swipe that pops this screen working while the navigation bar is hidden.
    /// UIKit turns the swipe off with the bar; this re-enables it only while this screen is on
    /// top, so screens that hide the bar to block leaving (Capture) keep blocking it.
    func keepsInteractivePop() -> some View {
        background(InteractivePopEnabler().frame(width: 0, height: 0).accessibilityHidden(true))
    }
}

private struct InteractivePopEnabler: UIViewControllerRepresentable {
    func makeUIViewController(context: Context) -> Controller { Controller() }
    func updateUIViewController(_ controller: Controller, context: Context) {}

    final class Controller: UIViewController, UIGestureRecognizerDelegate {
        private weak var originalDelegate: (any UIGestureRecognizerDelegate)?
        private weak var gesture: UIGestureRecognizer?

        override func viewDidAppear(_ animated: Bool) {
            super.viewDidAppear(animated)
            guard let gesture = navigationController?.interactivePopGestureRecognizer, gesture.delegate !== self else { return }
            originalDelegate = gesture.delegate
            self.gesture = gesture
            gesture.delegate = self
        }

        override func viewWillDisappear(_ animated: Bool) {
            super.viewWillDisappear(animated)
            if let gesture, gesture.delegate === self { gesture.delegate = originalDelegate }
            gesture = nil
        }

        func gestureRecognizerShouldBegin(_ gestureRecognizer: UIGestureRecognizer) -> Bool {
            guard let navigationController else { return false }
            return navigationController.viewControllers.count > 1 && navigationController.transitionCoordinator == nil
        }
    }
}
