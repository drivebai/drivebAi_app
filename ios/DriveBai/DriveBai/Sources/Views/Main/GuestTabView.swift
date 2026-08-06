import SwiftUI

// MARK: - Guest Tab View (guest mode)
//
// The signed-out shell: Turo-style, the app opens straight into browsing.
// The bar mirrors the authenticated shell — Today / Discover / Chats /
// Profile, same icons and tags — so guests see the app they're signing up
// for. Discover is fully browsable (server-redacted payload); the three
// gated tabs embed the OTP flow as their tab root, never a wall, each with
// a context line explaining what lives behind it. Gated CTAs inside
// Discover still raise `DeepLinkRouter.guestPrompt`, presented here as a
// dismissible sheet with a conversion context line.
//
// Deliberately absent for guests: the Ask-for-Help floating button
// (support chat is per-user; anonymous support is a flagged follow-up,
// not built).
struct GuestTabView: View {
    @EnvironmentObject private var deepLinkRouter: DeepLinkRouter
    @ObservedObject private var guestStore = GuestOnboardingStore.shared
    /// Same tags as DriverTabView; guests open on Discover (tag 1) so the
    /// first screen is still browsing, not a sign-in prompt.
    @State private var selectedTab = 1

    var body: some View {
        TabView(selection: $selectedTab) {
            gatedTab(
                title: "Sign in to see your day",
                body: "Your rentals, pickups and tasks live here."
            )
            .tabItem {
                Label("Today", systemImage: "house.fill")
            }
            .tag(0)

            DiscoverView()
                .tabItem {
                    Label("Discover", systemImage: "car.2.fill")
                }
                .tag(1)

            gatedTab(
                title: "Sign in to see your chats",
                body: "Messages with owners and renters live here."
            )
            .tabItem {
                Label("Chats", systemImage: "message.fill")
            }
            .tag(2)

            profileTab
                .tabItem {
                    Label("Profile", systemImage: "person.fill")
                }
                .tag(3)
        }
        .tint(.driveBaiPrimary)
        // Conversion prompt raised by a gated CTA (heart / rent / buy /
        // exact location). Sheet-presented so the guest's place in Discover
        // is untouched on dismiss; on successful sign-in the root swaps to
        // the authenticated tabs and the intent replays (unit 3).
        .sheet(item: $deepLinkRouter.guestPrompt) { prompt in
            EnterEmailOTPView(
                showDismissButton: true,
                contextTitle: prompt.title,
                contextBody: prompt.body
            )
        }
    }

    /// A gated tab embeds the existing OTP entry as its root with copy naming
    /// what the tab holds — the same return-to-context machinery as the old
    /// Sign-in tab, so nothing downstream (capture/replay) changes.
    private func gatedTab(title: String, body: String) -> some View {
        EnterEmailOTPView(
            showDismissButton: false,
            contextTitle: title,
            contextBody: body
        )
    }

    /// The Profile tab absorbs the old Sign-in tab's two-state treatment:
    /// first-time visitors get the value/trust intro above the email field,
    /// returning visitors go straight to a "Welcome back" email prompt.
    @ViewBuilder
    private var profileTab: some View {
        if guestStore.hasEnteredEmailBefore {
            EnterEmailOTPView(
                showDismissButton: false,
                contextTitle: "Welcome back",
                contextBody: "Enter your email to sign in."
            )
        } else {
            EnterEmailOTPView(showDismissButton: false, showGuestIntro: true)
        }
    }
}

// MARK: - Guest Onboarding Nudge
//
// The single pushed element of guest onboarding: a gentle, dismissible card
// shown once per install after the guest has browsed several cars. It never
// blocks scrolling, "Not now" silences it permanently in one tap, and it is
// the only nudge — no timers, no scarcity, no disguised dismiss.
struct GuestOnboardingNudge: View {
    let onLearnMore: () -> Void
    let onDismiss: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "shield.lefthalf.filled")
                .font(.system(size: 20, weight: .semibold))
                .foregroundColor(.driveBaiPrimary)

            VStack(alignment: .leading, spacing: 4) {
                Text("Seeing something you like?")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary)
                Text("Here's how renting and buying work on DriveBai — and what stays private.")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)

                Button(action: onLearnMore) {
                    Text("How it works")
                        .font(.caption.weight(.semibold))
                        .foregroundColor(.driveBaiPrimary)
                }
                .padding(.top, 2)
            }

            Spacer(minLength: 0)

            Button(action: onDismiss) {
                Image(systemName: "xmark")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundColor(.secondary)
                    .frame(width: 28, height: 28)
            }
            .accessibilityLabel("Not now")
        }
        .padding(14)
        .background(
            RoundedRectangle(cornerRadius: 16)
                .fill(Color(.systemBackground))
                .shadow(color: .black.opacity(0.15), radius: 10, y: 4)
        )
        .overlay(
            RoundedRectangle(cornerRadius: 16)
                .stroke(Color.driveBaiPrimary.opacity(0.2), lineWidth: 1)
        )
    }
}
