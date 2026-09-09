import SwiftUI
import StripePaymentSheet

/// A zero-size SwiftUI view that presents Stripe PaymentSheet from the hosting VC.
/// Use as a `.background()` overlay, NOT inside `.sheet()`, to avoid double-modal issues.
struct PaymentSheetPresenter: UIViewControllerRepresentable {
    let clientSecret: String
    let ephemeralKeySecret: String
    let customerId: String
    let publishableKey: String
    let onResult: (PaymentSheetResult) -> Void

    func makeUIViewController(context: Context) -> UIViewController {
        let vc = UIViewController()
        vc.view.backgroundColor = .clear
        vc.view.isUserInteractionEnabled = false
        return vc
    }

    func updateUIViewController(_ uiViewController: UIViewController, context: Context) {
        guard !context.coordinator.didPresent else { return }
        context.coordinator.didPresent = true

        STPAPIClient.shared.publishableKey = publishableKey

        var config = PaymentSheet.Configuration()
        config.merchantDisplayName = "DriveBai"
        config.customer = .init(id: customerId, ephemeralKeySecret: ephemeralKeySecret)
        config.allowsDelayedPaymentMethods = false

        let paymentSheet = PaymentSheet(paymentIntentClientSecret: clientSecret, configuration: config)

        // Find a presenting VC that is actually in the window hierarchy
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
            guard let presentingVC = uiViewController.topMostViewController() else {
                self.onResult(.canceled)
                return
            }
            paymentSheet.present(from: presentingVC) { result in
                self.onResult(result)
            }
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator {
        var didPresent = false
    }
}

/// The same zero-size presenter, in SETUP mode: it saves a card without
/// charging it. Used by the rolling billing card to replace the card a
/// weekly mandate charges. Kept in this file deliberately — a new Swift
/// file needs manual pbxproj registration in four places.
struct SetupSheetPresenter: UIViewControllerRepresentable {
    let setupIntentClientSecret: String
    let ephemeralKeySecret: String
    let customerId: String
    let publishableKey: String
    let onResult: (PaymentSheetResult) -> Void

    func makeUIViewController(context: Context) -> UIViewController {
        let vc = UIViewController()
        vc.view.backgroundColor = .clear
        vc.view.isUserInteractionEnabled = false
        return vc
    }

    func updateUIViewController(_ uiViewController: UIViewController, context: Context) {
        guard !context.coordinator.didPresent else { return }
        context.coordinator.didPresent = true

        STPAPIClient.shared.publishableKey = publishableKey

        var config = PaymentSheet.Configuration()
        config.merchantDisplayName = "DriveBai"
        config.customer = .init(id: customerId, ephemeralKeySecret: ephemeralKeySecret)
        // A weekly mandate charges off-session; delayed-notification methods
        // cannot honour that, so the card form stays card-only here.
        config.allowsDelayedPaymentMethods = false

        let sheet = PaymentSheet(setupIntentClientSecret: setupIntentClientSecret, configuration: config)

        DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
            guard let presentingVC = uiViewController.topMostViewController() else {
                self.onResult(.canceled)
                return
            }
            sheet.present(from: presentingVC) { result in
                self.onResult(result)
            }
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator {
        var didPresent = false
    }
}

private extension UIViewController {
    /// Walk up the presentation chain to find the topmost VC that can present.
    func topMostViewController() -> UIViewController? {
        // Start from the window's root VC for reliability
        guard let windowScene = UIApplication.shared.connectedScenes
            .compactMap({ $0 as? UIWindowScene }).first,
              let rootVC = windowScene.windows.first(where: { $0.isKeyWindow })?.rootViewController else {
            return self
        }
        var top = rootVC
        while let presented = top.presentedViewController {
            top = presented
        }
        return top
    }
}
