import UIKit

// MARK: - Measured overlay geometry (never assumed)
//
// The coach-mark overlay draws in full-screen space (its GeometryReader ignores
// the safe area so the scrim covers the whole window and the tab cutout can sit
// over the real bar). The native UITabBar lives outside SwiftUI's preference
// tree, so its cell rects can't be tagged with `.onboardingTarget`; instead we
// read the REAL bar/button frames from UIKit at draw time.
//
// Why measured, not a 49pt constant: the tab bar's height, shape, and vertical
// position are not stable across iOS versions or devices — the iOS 26
// liquid-glass bar floats with its own inset and corner radius, an older bar is
// opaque and flush to the home indicator, and iPad centers its items. A rect
// derived from a constant is right on one configuration and quietly wrong
// everywhere else. Reading the live view frame is right on all of them.
//
// Frames are returned in the key window's coordinate space, which — for a
// full-screen phone app whose overlay ignores the safe area — coincides with
// the overlay GeometryReader's local space (origin at the window's top-left).
enum OverlayGeometry {
    static var keyWindow: UIWindow? {
        let windows = UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap { $0.windows }
        return windows.first { $0.isKeyWindow } ?? windows.first
    }

    /// The whole native tab bar's frame in window coordinates, or nil if none
    /// is on screen (e.g. a sheet is frontmost).
    static func tabBarFrame() -> CGRect? {
        guard let window = keyWindow, let bar = findTabBar(in: window) else { return nil }
        return bar.convert(bar.bounds, to: window)
    }

    /// The real per-cell (icon+label) frames of the tab bar, left-to-right, in
    /// window coordinates. Empty when no bar / cells can be read — the caller
    /// then draws a centered, arrow-less card rather than guessing.
    static func tabCellFrames() -> [CGRect] {
        guard let window = keyWindow, let bar = findTabBar(in: window) else { return [] }
        return topLevelControls(of: bar)
            .filter { !$0.bounds.isEmpty && $0.isUserInteractionEnabled }
            .map { $0.convert($0.bounds, to: window) }
            .sorted { $0.midX < $1.midX }
    }

    // MARK: - Traversal

    private static func findTabBar(in view: UIView) -> UITabBar? {
        if let bar = view as? UITabBar { return bar }
        for sub in view.subviews {
            if let bar = findTabBar(in: sub) { return bar }
        }
        return nil
    }

    // Tab cells are the bar's UIControl descendants (UITabBarButton is private;
    // matching UIControl is stable across iOS versions and the liquid-glass
    // bar). Stop descending once a control is found so a control that itself
    // contains controls is never double-counted.
    private static func topLevelControls(of view: UIView) -> [UIControl] {
        var out: [UIControl] = []
        for sub in view.subviews {
            if let control = sub as? UIControl {
                out.append(control)
            } else {
                out.append(contentsOf: topLevelControls(of: sub))
            }
        }
        return out
    }
}
