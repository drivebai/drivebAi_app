import SwiftUI

// MARK: - How DriveBai Works (guest trust surface)
//
// The pull-based trust story for guests: reachable from the Sign-in tab and
// the engagement nudge, never pushed on launch. Scannable by design — each
// point is a short bold claim the eye catches, with at most one quieter line
// beneath where honesty needs it. Reading only the bold claims must leave a
// correct impression, so every bold line was re-checked against the
// verification round's do-not-claim list.
//
// Notably absent, on purpose: escrow, "released to the seller",
// verified/vetted/screened, title verification, DMV/lien/theft checks, social
// proof, star averages, deposit protection. "seller-declared", not "verified".
// "your card isn't charged until", not "you don't pay until". The privacy
// claim is deliberately NOT compressed to "your documents stay private" — the
// download is bearer-of-link and admins receive links too, so the honest form
// (who gets a link + it expires) stays two lines.
//
// The bullet marker is a neutral dot, never a checkmark: a check would imply a
// verification we don't perform.
//
// A "Who you're dealing with" section was deliberately omitted: we don't vet,
// screen, or background-check people, and the ratings system is empty. The two
// honest pieces live elsewhere (email-verified sign-in under "Start free"; the
// owner reviewing your license under "Renting"). It returns when licenses are
// actually being reviewed in the admin dashboard.
//
// The DMV callout is the client's own "we don't want buyers blaming the app"
// framing (7/12 #11). Both sentences are intact and given a distinct, more
// noticeable treatment. Do not shorten, soften, or move it out of view.
struct HowDriveBaiWorksView: View {
    @Environment(\.dismiss) private var dismiss
    /// Called when the guest taps "Sign up to list your car". The presenter
    /// decides what happens (the Sign-in tab just sets the owner role hint;
    /// the nudge dismisses and raises the sign-in prompt).
    var onSignUpAsOwner: () -> Void

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    ForEach(Array(Self.sections.enumerated()), id: \.element.id) { index, section in
                        if index > 0 {
                            Divider().padding(.vertical, 2)
                        }
                        sectionView(section)
                    }

                    Divider().padding(.vertical, 2)
                    ownerCallout
                }
                .padding(20)
                .padding(.bottom, 24)
            }
            .navigationTitle("How DriveBai works")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") { dismiss() }
                }
            }
        }
    }

    // MARK: - Section + bullet rendering

    private func sectionView(_ section: TrustSection) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 10) {
                Image(systemName: section.icon)
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 26)
                Text(section.title)
                    .font(.headline)
            }

            VStack(alignment: .leading, spacing: 12) {
                ForEach(section.bullets) { bulletRow($0) }
            }

            if section.showDMVCallout {
                dmvCallout
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func bulletRow(_ bullet: Bullet) -> some View {
        HStack(alignment: .top, spacing: 10) {
            // Neutral anchor — never a checkmark (would imply verification).
            Circle()
                .fill(Color.driveBaiPrimary)
                .frame(width: 6, height: 6)
                .padding(.top, 6)
            VStack(alignment: .leading, spacing: 3) {
                Text(bullet.claim)
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary)
                    .fixedSize(horizontal: false, vertical: true)
                if let support = bullet.support {
                    Text(support)
                        .font(.footnote)
                        .foregroundColor(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // The limits callout — both sentences intact, in full primary color, with
    // a warning tint so a scanning reader can't miss it.
    private var dmvCallout: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.system(size: 16, weight: .semibold))
                .foregroundColor(.orange)
            VStack(alignment: .leading, spacing: 4) {
                Text("DriveBai structures the paperwork and payment — it doesn't run DMV, title, lien, or theft checks.")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary)
                Text("Confirm the title with your local DMV before you register the car.")
                    .font(.subheadline)
                    .foregroundColor(.primary)
            }
            .fixedSize(horizontal: false, vertical: true)
        }
        .padding(12)
        .background(RoundedRectangle(cornerRadius: 12).fill(Color.orange.opacity(0.08)))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color.orange.opacity(0.35), lineWidth: 1))
        .padding(.top, 4)
    }

    private var ownerCallout: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 10) {
                Image(systemName: "car.2.fill")
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 26)
                Text("Thinking of listing your car?")
                    .font(.headline)
            }

            VStack(alignment: .leading, spacing: 12) {
                ForEach(Self.ownerBullets) { bulletRow($0) }
            }

            Button(action: onSignUpAsOwner) {
                Text("Sign up to list your car")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.white)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 12)
                    .background(Color.driveBaiPrimary)
                    .cornerRadius(12)
            }
            .padding(.top, 4)
        }
        .padding(16)
        .background(Color.driveBaiPrimary.opacity(0.06))
        .cornerRadius(16)
    }

    // MARK: - Content (verified copy; client will do a wording pass)

    private struct Bullet: Identifiable {
        let id = UUID()
        let claim: String
        var support: String? = nil
    }

    private struct TrustSection: Identifiable {
        let id = UUID()
        let icon: String
        let title: String
        let bullets: [Bullet]
        var showDMVCallout: Bool = false
    }

    private static let sections: [TrustSection] = [
        TrustSection(
            icon: "hand.raised.fill",
            title: "Start free. Share details only when you act.",
            bullets: [
                Bullet(claim: "Browse every car without an account."),
                Bullet(claim: "Sign up with just your email.",
                       support: "Verified by a code we email you."),
                Bullet(claim: "A license only when you drive. A payment method only when you book or buy."),
            ]
        ),
        TrustSection(
            icon: "lock.shield.fill",
            title: "Your information stays private",
            bullets: [
                Bullet(claim: "Your license, ID, and paperwork never appear on a public page."),
                Bullet(claim: "Only you, the other party, and DriveBai support get a link to them.",
                       support: "Every link expires within an hour."),
                Bullet(claim: "List a car, and browsers never see your exact address or last name."),
                Bullet(claim: "Your card details go to Stripe, never to us."),
            ]
        ),
        TrustSection(
            icon: "doc.text.fill",
            title: "Buying a car",
            bullets: [
                Bullet(claim: "You both sign a Bill of Sale in the app."),
                Bullet(claim: "The seller declares the title's status and uploads the title for you to review."),
                Bullet(claim: "You inspect the car and tick off an 8-point checklist before the sale is final."),
                Bullet(claim: "Your card is authorized when you agree — charged only after you accept the car."),
            ],
            showDMVCallout: true
        ),
        TrustSection(
            icon: "key.fill",
            title: "Renting a car",
            bullets: [
                Bullet(claim: "Book and pay for your dates up front."),
                Bullet(claim: "Owner misses the pickup deadline? You're refunded in full, automatically."),
                Bullet(claim: "Return early and your unused full days are refunded once the owner confirms the car's back."),
                Bullet(claim: "The owner reviews your license before accepting — it's never shown publicly."),
            ]
        ),
    ]

    private static let ownerBullets: [Bullet] = [
        Bullet(claim: "You choose who rents — review each license, approve each request."),
        Bullet(claim: "Your exact address stays hidden from browsers."),
        Bullet(claim: "No listing goes live until a DriveBai admin approves it."),
        Bullet(claim: "You set your own price and requirements."),
    ]
}
