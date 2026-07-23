import SwiftUI

// MARK: - Guest Tab View (guest mode)
//
// The signed-out shell: Turo-style, the app opens straight into browsing.
// Two tabs — Discover (the real listings, server-redacted payload) and
// Sign in (the OTP flow embedded as a tab root, never a wall). Gated CTAs
// inside Discover raise `DeepLinkRouter.guestPrompt`, presented here as a
// dismissible sheet with a conversion context line.
//
// Deliberately absent for guests: Today, Chats, the Ask-for-Help floating
// button (support chat is per-user; anonymous support is a flagged
// follow-up, not built).
struct GuestTabView: View {
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter
    @State private var selectedTab = 0

    var body: some View {
        TabView(selection: $selectedTab) {
            DiscoverView()
                .tabItem {
                    Image(systemName: "car.2.fill")
                    Text("Discover")
                }
                .tag(0)

            EnterEmailOTPView(showDismissButton: false)
                .tabItem {
                    Image(systemName: "person.crop.circle")
                    Text("Sign in")
                }
                .tag(1)
        }
        .tint(.driveBaiPrimary)
        // Conversion prompt raised by a gated CTA (heart / rent / buy /
        // exact location). Sheet-presented so the guest's place in Discover
        // is untouched on dismiss; on successful sign-in the root swaps to
        // the authenticated tabs and the intent replays (unit 3).
        .sheet(item: $deepLinkRouter.guestPrompt) { prompt in
            EnterEmailOTPView(showDismissButton: true, contextMessage: prompt.message)
        }
    }
}
