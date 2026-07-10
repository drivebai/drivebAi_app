import SwiftUI
import UIKit

// MARK: - Anchor preference plumbing
//
// In-screen targets are tagged with `.onboardingTarget(_:)`, which publishes a
// bounds anchor keyed by `TourTargetID`. The host reduces these into the
// overlay's coordinate space via `overlayPreferenceValue`. Tab targets are the
// exception — they are resolved geometrically (see TabBarSpotlight) because the
// native `UITabBar` lives outside the preference tree.

struct TourTargetPreferenceKey: PreferenceKey {
    static var defaultValue: [TourTargetID: Anchor<CGRect>] = [:]
    static func reduce(value: inout Value, nextValue: () -> Value) {
        value.merge(nextValue()) { $1 }
    }
}

extension View {
    /// Tag this view as a coach-mark target. When a tour step points at `id`,
    /// the overlay spotlights this view's bounds.
    func onboardingTarget(_ id: TourTargetID) -> some View {
        anchorPreference(key: TourTargetPreferenceKey.self, value: .bounds) { [id: $0] }
    }

    /// Claim a coach-mark target only when `id` is non-nil. Use this for
    /// controls that are sometimes disabled or absent — an unclaimed target
    /// resolves to nil and the card falls back to a centred, arrow-less form
    /// rather than pointing at something the user cannot use.
    @ViewBuilder
    func onboardingTarget(_ id: TourTargetID?) -> some View {
        if let id {
            anchorPreference(key: TourTargetPreferenceKey.self, value: .bounds) { [id: $0] }
        } else {
            self
        }
    }
}

// MARK: - Arrow shape

struct TourArrowTriangle: Shape {
    let direction: ArrowDirection
    func path(in rect: CGRect) -> Path {
        var p = Path()
        switch direction {
        case .up:
            p.move(to: CGPoint(x: rect.midX, y: rect.minY))
            p.addLine(to: CGPoint(x: rect.minX, y: rect.maxY))
            p.addLine(to: CGPoint(x: rect.maxX, y: rect.maxY))
        case .down, .none:
            p.move(to: CGPoint(x: rect.midX, y: rect.maxY))
            p.addLine(to: CGPoint(x: rect.minX, y: rect.minY))
            p.addLine(to: CGPoint(x: rect.maxX, y: rect.minY))
        }
        p.closeSubpath()
        return p
    }
}

// MARK: - Coach-mark scrim
//
// The full-screen dim + punched spotlight + floating coach card. Pure
// presentation — it takes a resolved spotlight rect (or nil) and callbacks, so
// it can be previewed without a coordinator.

struct CoachMarkScrim: View {
    let step: TourStep
    /// Resolved target rect in the overlay's coordinate space; `nil` ⇒ dim-only
    /// centered card with no cutout or arrow (never point at empty space).
    let spotlight: CGRect?
    let containerSize: CGSize
    let safeInsets: EdgeInsets
    /// "Step i of n" for multi-step tours, else `nil`.
    let progressText: String?
    let onPrimary: () -> Void
    let onSkip: () -> Void

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @State private var cardSize: CGSize = .zero
    @State private var pulse = false
    @AccessibilityFocusState private var cardFocused: Bool

    private let gap: CGFloat = 14
    private let sideMargin: CGFloat = 16
    private let dimOpacity: Double = 0.62
    private let cardMaxWidth: CGFloat = 360

    var body: some View {
        let place = placement

        return ZStack(alignment: .topLeading) {
            // 1. Dim + punched hole (non-interactive).
            dimLayer
                .allowsHitTesting(false)

            // 2. Tap-anywhere-to-advance — a real, focusable button.
            Button(action: onPrimary) {
                Rectangle().fill(Color.clear).contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .frame(width: containerSize.width, height: containerSize.height)
            .accessibilityLabel(accessibilitySummary)
            .accessibilityHint("Double tap to continue")

            // 3. Pulsing outline (skipped under Reduce Motion).
            if let rect = spotlight, !reduceMotion {
                pulseRing(rect).allowsHitTesting(false)
            }

            // 4. Arrow pointing from the card toward the target.
            if let ax = place.arrowX, place.arrow != .none, cardSize.height > 0 {
                TourArrowTriangle(direction: place.arrow)
                    .fill(cardBackground)
                    .frame(width: 24, height: 12)
                    .position(x: ax, y: arrowY(for: place))
                    .allowsHitTesting(false)
            }

            // 5. The coach card.
            card
                .frame(maxWidth: cardMaxWidth)
                .background(cardSizeReader)
                .position(place.center)
                .accessibilityElement(children: .contain)
                .accessibilityFocused($cardFocused)

            // 6. Skip affordance (top-right).
            if step.canSkip {
                skipButton
                    .position(x: containerSize.width - sideMargin - 30,
                              y: safeInsets.top + 24)
            }
        }
        .frame(width: containerSize.width, height: containerSize.height)
        .accessibilityAddTraits(.isModal)
        .onAppear {
            startPulse()
            focusCard()
        }
        .onChange(of: step.id) { _, _ in
            focusCard()
            announce()
        }
    }

    // MARK: Layers

    private var dimLayer: some View {
        ZStack {
            Rectangle().fill(Color.black.opacity(dimOpacity))
            if let rect = spotlight {
                cutoutPath(rect)
                    .foregroundColor(.black)
                    .blendMode(.destinationOut)
            }
        }
        .compositingGroup()
    }

    private func pulseRing(_ rect: CGRect) -> some View {
        let spot = spotRect(rect)
        return RoundedRectangle(cornerRadius: cornerRadius(spot))
            .stroke(Color.white.opacity(0.85), lineWidth: 2)
            .frame(width: spot.width, height: spot.height)
            .position(x: rect.midX, y: rect.midY)
            .opacity(pulse ? 0.25 : 0.9)
            .animation(.easeInOut(duration: 1.1).repeatForever(autoreverses: true), value: pulse)
    }

    private var card: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let progressText {
                Text(progressText)
                    .font(.caption).fontWeight(.semibold)
                    .foregroundColor(.secondary)
                    .accessibilityHidden(true)
            }
            Text(step.title)
                .font(.headline)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
            bodyText
            HStack(spacing: 12) {
                Spacer(minLength: 0)
                Button(action: onPrimary) {
                    Text(step.primaryTitle).fontWeight(.semibold)
                }
                .buttonStyle(.borderedProminent)
                .tint(.driveBaiPrimary)
            }
        }
        .padding(16)
        .background(
            RoundedRectangle(cornerRadius: 16)
                .fill(cardBackground)
                .shadow(color: .black.opacity(0.25), radius: 16, y: 6)
        )
    }

    @ViewBuilder
    private var bodyText: some View {
        if dynamicTypeSize.isAccessibilitySize {
            // At accessibility sizes let the body scroll rather than clip.
            ScrollView {
                Text(step.body)
                    .font(.body)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxHeight: 220)
        } else {
            Text(step.body)
                .font(.body)
                .foregroundColor(.primary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var skipButton: some View {
        Button(action: onSkip) {
            Text("Skip tour")
                .font(.subheadline).fontWeight(.medium)
                .padding(.horizontal, 12).padding(.vertical, 7)
                .background(Capsule().fill(.ultraThinMaterial))
                .foregroundColor(.primary)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Skip tour")
    }

    // MARK: Placement

    private struct Placement {
        var center: CGPoint
        var arrow: ArrowDirection
        var arrowX: CGFloat?
    }

    private var effectiveCardWidth: CGFloat {
        min(cardMaxWidth, containerSize.width - 2 * sideMargin)
    }

    private var placement: Placement {
        let h = cardSize.height
        guard let rect = spotlight else {
            return Placement(
                center: CGPoint(x: containerSize.width / 2, y: containerSize.height / 2),
                arrow: .none, arrowX: nil
            )
        }
        let below = rect.midY < containerSize.height * 0.5
        let arrow: ArrowDirection = below ? .up : .down

        var topY = below ? (rect.maxY + gap) : (rect.minY - gap - h)
        let minTop = safeInsets.top + gap
        let maxTop = max(minTop, containerSize.height - safeInsets.bottom - gap - h)
        topY = min(max(topY, minTop), maxTop)

        let centerX = containerSize.width / 2
        let w = effectiveCardWidth
        let cardLeft = centerX - w / 2
        let arrowX = min(max(rect.midX, cardLeft + 22), cardLeft + w - 22)

        return Placement(
            center: CGPoint(x: centerX, y: topY + h / 2),
            arrow: arrow,
            arrowX: arrowX
        )
    }

    private func arrowY(for place: Placement) -> CGFloat {
        let cardTop = place.center.y - cardSize.height / 2
        let cardBottom = place.center.y + cardSize.height / 2
        return place.arrow == .up ? (cardTop - 5) : (cardBottom + 5)
    }

    // MARK: Cutout geometry

    private func spotRect(_ rect: CGRect) -> CGRect { rect.insetBy(dx: -8, dy: -8) }

    private func cutoutPath(_ rect: CGRect) -> Path {
        RoundedRectangle(cornerRadius: cornerRadius(spotRect(rect)))
            .path(in: spotRect(rect))
    }

    /// Capsule-ish for short wide targets (tab cells / pills), soft rectangle
    /// otherwise.
    private func cornerRadius(_ rect: CGRect) -> CGFloat {
        if case .tab = step.target { return min(rect.width, rect.height) / 2 }
        return 14
    }

    // MARK: Cosmetics + a11y

    private var cardBackground: Color { Color(.systemBackground) }

    private var accessibilitySummary: String {
        "\(step.title). \(step.body). \(step.primaryTitle) button."
    }

    private var cardSizeReader: some View {
        GeometryReader { g in
            Color.clear
                .onAppear { cardSize = g.size }
                .onChange(of: g.size) { _, s in cardSize = s }
        }
    }

    private func startPulse() {
        guard !reduceMotion else { return }
        pulse = true
    }

    private func focusCard() {
        DispatchQueue.main.async { cardFocused = true }
    }

    private func announce() {
        UIAccessibility.post(notification: .screenChanged, argument: step.title)
    }
}

// MARK: - Overlay host
//
// Installs the coach-mark layer above a screen (or a sheet). It is applied
// TWICE — once at each `TabView` root and again at the root of every sheet that
// contains a target — and every instance observes the SAME coordinator. To
// avoid double-dimming when a sheet is stacked over a tab root, only the
// frontmost registered host draws the scrim (tracked by a host token). A sheet
// gets a fresh environment, so callers must re-inject the coordinator with
// `.environmentObject(coord)` inside the sheet.

struct OnboardingOverlayHost: ViewModifier {
    @ObservedObject var coord: ProductTourCoordinator
    @Environment(\.horizontalSizeClass) private var horizontalSizeClass
    @State private var hostToken: Int = 0

    func body(content: Content) -> some View {
        content
            .overlayPreferenceValue(TourTargetPreferenceKey.self) { anchors in
                GeometryReader { proxy in
                    overlay(anchors: anchors, proxy: proxy)
                }
                .ignoresSafeArea()
            }
            // SwiftUI does not promise balanced appear/disappear pairs. Register
            // exactly once per mounted host: a duplicate onAppear would push a
            // second token, and unregistering only the newest would leave a
            // stale one frontmost — suppressing the scrim on a live host.
            .onAppear { if hostToken == 0 { hostToken = coord.registerHost() } }
            .onDisappear {
                guard hostToken != 0 else { return }
                coord.unregisterHost(hostToken)
                hostToken = 0
            }
    }

    @ViewBuilder
    private func overlay(anchors: [TourTargetID: Anchor<CGRect>], proxy: GeometryProxy) -> some View {
        if let step = coord.currentStep, !coord.isStepDismissed, coord.frontmostHostToken == hostToken {
            let rect = resolveRect(step: step, anchors: anchors, proxy: proxy)
            CoachMarkScrim(
                step: step,
                spotlight: rect,
                containerSize: proxy.size,
                safeInsets: proxy.safeAreaInsets,
                progressText: coord.showsProgress ? "Step \(coord.stepNumber) of \(coord.stepCount)" : nil,
                onPrimary: { coord.next() },
                onSkip: { coord.skip() }
            )
            .id(step.id) // fresh a11y focus per step; cross-fade under Reduce Motion.
            .transition(.opacity)
        }
    }

    /// Number of cells in both tab bars (driver and owner).
    private static let tabCount = 4

    /// Resolve a step's target to a rect in the overlay's space:
    /// - `nil` target ⇒ nil (centered dim-only card),
    /// - `.tab(_)` ⇒ geometric cell rect, or nil on layouts whose bar we can't
    ///   describe (iPad / landscape),
    /// - in-screen id ⇒ the preference anchor, or nil if scrolled off / torn
    ///   down (dim-only, never a dangling arrow).
    private func resolveRect(
        step: TourStep,
        anchors: [TourTargetID: Anchor<CGRect>],
        proxy: GeometryProxy
    ) -> CGRect? {
        guard let target = step.target else { return nil }
        if case let .tab(tab) = target {
            return TabBarSpotlightRect.rect(
                tabIndex: tab.index,
                tabCount: Self.tabCount,
                in: proxy,
                horizontalSizeClass: horizontalSizeClass
            )
        }
        if let anchor = anchors[target] {
            return proxy[anchor]
        }
        return nil
    }
}

extension View {
    /// Install the coach-mark overlay for `coord` on this view. Apply at each
    /// `TabView` root and at the root of any sheet that contains a target.
    func onboardingOverlayHost(_ coord: ProductTourCoordinator) -> some View {
        modifier(OnboardingOverlayHost(coord: coord))
    }
}

// MARK: - Previews

#if DEBUG
private struct CoachMarkPreviewWrapper: View {
    let spotlight: CGRect?
    let step: TourStep
    var progress: String?

    var body: some View {
        GeometryReader { proxy in
            ZStack {
                // Fake underlying screen content.
                LinearGradient(colors: [.teal.opacity(0.3), .blue.opacity(0.2)],
                               startPoint: .top, endPoint: .bottom)
                VStack {
                    Text("Underlying screen").font(.title2).padding(.top, 80)
                    Spacer()
                }
                CoachMarkScrim(
                    step: step,
                    spotlight: spotlight,
                    containerSize: proxy.size,
                    safeInsets: proxy.safeAreaInsets,
                    progressText: progress,
                    onPrimary: {},
                    onSkip: {}
                )
            }
            .ignoresSafeArea()
        }
    }
}

#Preview("Coach card · spotlight top") {
    CoachMarkPreviewWrapper(
        spotlight: CGRect(x: 24, y: 120, width: 200, height: 44),
        step: TourStep(
            id: "search", tour: .driverFirstDiscover,
            title: "Search & filter",
            body: "Find cars by name, price or location.",
            target: .discoverSearch, arrow: .up, cardPlacement: .below,
            primaryTitle: "Next"
        ),
        progress: "Step 1 of 2"
    )
}

#Preview("Coach card · centered") {
    CoachMarkPreviewWrapper(
        spotlight: nil,
        step: TourStep(
            id: "welcome", tour: .sharedWelcome,
            title: "Welcome to DrivaBai",
            body: "Rent or list a car with someone real, nearby. This quick tour shows you around.",
            target: nil, arrow: .none, cardPlacement: .centered,
            primaryTitle: "Get started"
        ),
        progress: nil
    )
}

#Preview("Coach card · tab spotlight") {
    GeometryReader { proxy in
        CoachMarkScrim(
            step: TourStep(
                id: "discover", tour: .driverTabs,
                title: "Discover",
                body: "Find a car to rent, right here.",
                target: .tab(.discover), arrow: .down, cardPlacement: .above,
                primaryTitle: "Next"
            ),
            spotlight: TabBarSpotlightRect.rect(
                tabIndex: 1, tabCount: 4, in: proxy, horizontalSizeClass: .compact
            ),
            containerSize: proxy.size,
            safeInsets: proxy.safeAreaInsets,
            progressText: "Step 2 of 4",
            onPrimary: {},
            onSkip: {}
        )
    }
    .ignoresSafeArea()
}
#endif
