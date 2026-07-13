import SwiftUI

// MARK: - Tab-bar spotlight geometry
//
// Native `.tabItem` content is rendered by UIKit's `UITabBar`, OUTSIDE
// SwiftUI's preference tree, so `.anchorPreference` cannot tag it and swapping
// to a custom bar would regress the live `.badge()` modifiers and the
// `selectedTab` deep-link `.onChange` handlers. Instead we compute the tab
// cell rect geometrically — the native bar is untouched.
//
// The 49pt bar height and the equal-width division only describe a compact-width
// portrait phone bar. On a regular-width layout (iPad) UIKit centres the items
// instead of spreading them, and a compact-height phone in landscape uses a
// shorter, inset bar — in both cases the computed cell lands on empty space.
//
// So the geometry is *conditional*: when the assumption doesn't hold we return
// nil and the caller falls back to a centred, arrow-less card. A coach mark
// that highlights the wrong pixels is worse than one that highlights nothing.

enum TabBarSpotlightRect {
    /// Standard `UITabBar` content height, excluding the bottom safe-area inset.
    static let standardBarHeight: CGFloat = 49

    /// Whether the equal-width / 49pt assumption describes the current layout.
    static func supportsGeometricSpotlight(
        horizontalSizeClass: UserInterfaceSizeClass?,
        containerSize: CGSize
    ) -> Bool {
        guard horizontalSizeClass == .compact else { return false }   // iPad / split view
        return containerSize.height >= containerSize.width            // portrait only
    }

    /// The rect of tab cell `tabIndex` (0-based) within `tabCount` equal cells,
    /// expressed in `proxy`'s coordinate space. `nil` when the layout is one we
    /// cannot describe geometrically.
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
        // The tappable icon row is the 49pt band sitting ABOVE the bottom
        // safe-area (home-indicator) inset. Anchoring to the inset's top and
        // keeping the height at 49pt puts the cutout on the icons; adding the
        // inset to either would push the rect down into empty home-indicator
        // space and drop its centre below the real icon row.
        let cellWidth = proxy.size.width / CGFloat(count)
        let clampedIndex = min(max(tabIndex, 0), count - 1)
        return CGRect(
            x: cellWidth * CGFloat(clampedIndex),
            y: proxy.size.height - proxy.safeAreaInsets.bottom - standardBarHeight,
            width: cellWidth,
            height: standardBarHeight
        )
    }
}
