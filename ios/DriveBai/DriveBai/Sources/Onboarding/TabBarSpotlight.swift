import SwiftUI

// MARK: - Tab-bar spotlight geometry
//
// Native `.tabItem` content is rendered by UIKit's `UITabBar`, OUTSIDE
// SwiftUI's preference tree, so `.anchorPreference` cannot tag it and swapping
// to a custom bar would regress the live `.badge()` modifiers and the
// `selectedTab` deep-link `.onChange` handlers. Instead we MEASURE the real
// bar/cell frames from UIKit at draw time (see OverlayGeometry) — the native
// bar is untouched, and there is no hardcoded height or vertical position, so
// it is correct on the iOS 26 liquid-glass floating bar and on older bars
// alike.
//
// The equal-width division still only describes a compact-width portrait phone
// bar: on a regular-width layout (iPad) UIKit centres the items and in
// landscape the bar is shorter/inset. Those layouts fall back to nil (a
// centred, arrow-less card) — highlighting nothing beats highlighting the
// wrong pixels. Enabling them is a behaviour change, out of scope here.

enum TabBarSpotlightRect {
    /// Whether the equal-width portrait-phone assumption describes the layout.
    static func supportsGeometricSpotlight(
        horizontalSizeClass: UserInterfaceSizeClass?,
        containerSize: CGSize
    ) -> Bool {
        guard horizontalSizeClass == .compact else { return false }   // iPad / split view
        return containerSize.height >= containerSize.width            // portrait only
    }

    /// The rect of tab cell `tabIndex` (0-based), in the overlay's full-screen
    /// coordinate space (== the key window's, since the overlay ignores the
    /// safe area). `nil` when the layout can't be spotlighted or the bar can't
    /// be read.
    static func rect(
        tabIndex: Int,
        tabCount: Int,
        in proxy: GeometryProxy,
        horizontalSizeClass: UserInterfaceSizeClass?
    ) -> CGRect? {
        guard supportsGeometricSpotlight(
            horizontalSizeClass: horizontalSizeClass,
            containerSize: proxy.size
        ) else { return nil }

        let count = max(tabCount, 1)
        let clampedIndex = min(max(tabIndex, 0), count - 1)

        // Preferred: the real per-cell button frame — exact on the standard and
        // the liquid-glass floating bar. Fall back to the measured whole-bar
        // frame split into equal cells; nil if neither can be read.
        let cells = OverlayGeometry.tabCellFrames()
        if cells.count == count {
            return cells[clampedIndex]
        }
        if let bar = OverlayGeometry.tabBarFrame() {
            let cellWidth = bar.width / CGFloat(count)
            return CGRect(
                x: bar.minX + cellWidth * CGFloat(clampedIndex),
                y: bar.minY,
                width: cellWidth,
                height: bar.height
            )
        }
        return nil
    }
}
