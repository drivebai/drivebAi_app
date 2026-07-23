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
    @ObservedObject private var guestStore = GuestOnboardingStore.shared
    @State private var selectedTab = 0

    var body: some View {
        TabView(selection: $selectedTab) {
            DiscoverView()
                .tabItem {
                    Image(systemName: "car.2.fill")
                    Text("Discover")
                }
                .tag(0)

            signInTab
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
            EnterEmailOTPView(
                showDismissButton: true,
                contextTitle: prompt.title,
                contextBody: prompt.body
            )
        }
    }

    /// The Sign-in tab treats a first-time visitor differently from someone
    /// returning to log in. First time: the value/trust intro above the email
    /// field. Returning: straight to a "Welcome back" email prompt.
    @ViewBuilder
    private var signInTab: some View {
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
