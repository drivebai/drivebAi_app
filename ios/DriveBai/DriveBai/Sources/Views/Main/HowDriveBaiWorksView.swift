import SwiftUI

// MARK: - How DriveBai Works (guest trust surface)
//
// The pull-based trust story for guests: reachable from the Sign-in tab and
// the engagement nudge, never pushed on launch. Every claim here was verified
// against the code before it was written; the do-not-claim list is enforced by
// omission, not by softening. Notably absent, on purpose: escrow, "released to
// the seller", verified/vetted/screened, title verification, DMV/lien/theft
// checks, social proof, star averages, deposit protection.
//
// The DMV/limits paragraph in "Buying a car, structured" is deliberate and
// load-bearing — it is the client's own "we don't want buyers blaming the app"
// framing (7/12 #11), moved up front. Do not drop or soften it.
struct HowDriveBaiWorksView: View {
    @Environment(\.dismiss) private var dismiss
    /// Called when the guest taps "Sign up to list your car". The presenter
    /// decides what happens (the Sign-in tab just sets the owner role hint;
    /// the nudge dismisses and raises the sign-in prompt).
    var onSignUpAsOwner: () -> Void

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 28) {
                    ForEach(Self.sections) { section in
                        sectionView(section)
                    }

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

    private func sectionView(_ section: TrustSection) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                Image(systemName: section.icon)
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 26)
                Text(section.title)
                    .font(.headline)
            }
            ForEach(Array(section.paragraphs.enumerated()), id: \.offset) { _, para in
                Text(para)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
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
            Text("You choose who rents — you review each driver's license and approve every request. Your exact address stays hidden from anyone browsing. You set your own price and requirements, and no listing goes live until a DriveBai admin approves it.")
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

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

    private struct TrustSection: Identifiable {
        let id = UUID()
        let icon: String
        let title: String
        let paragraphs: [String]
    }

    private static let sections: [TrustSection] = [
        TrustSection(
            icon: "hand.raised.fill",
            title: "Only what you need, when you need it",
            paragraphs: [
                "Browse everything with no account. Sign up with just your email to save cars or message owners. Add a driver's license only when you're ready to drive, and a payment method only when you book or buy. Nothing sensitive up front."
            ]
        ),
        TrustSection(
            icon: "lock.shield.fill",
            title: "Your information stays private",
            paragraphs: [
                "Your driver's license, ID, and paperwork never appear on any public page. We create temporary links to them that expire within an hour, and only for the people in your transaction. When you list a car, we hide your exact address and last name from anyone browsing without an account."
            ]
        ),
        TrustSection(
            icon: "doc.text.fill",
            title: "Buying a car, structured",
            paragraphs: [
                "Every sale includes a Bill of Sale you both sign in the app, the seller's declared title status, the actual title document for you to review, and an in-person inspection checklist you complete before the sale is final. Your card is authorized when you agree to buy, and only charged after you accept the car.",
                "DriveBai structures the paperwork and the payment — it doesn't run DMV, title, lien, or theft checks. Confirm the title with your local DMV before you register the car."
            ]
        ),
        TrustSection(
            icon: "key.fill",
            title: "Renting a car",
            paragraphs: [
                "Book and pay for your dates up front. If the owner doesn't hand over the car by the pickup deadline, you're automatically refunded in full. Return early and your unused full days are refunded once the owner confirms the car's back."
            ]
        ),
        TrustSection(
            icon: "person.2.fill",
            title: "Who you're dealing with",
            paragraphs: [
                "Drivers add a driver's license the owner reviews before accepting a rental. Signing in is verified by email. Ratings come only from completed rentals and sales — one per transaction — so a rating always reflects a real trip."
            ]
        ),
        TrustSection(
            icon: "exclamationmark.shield.fill",
            title: "If something goes wrong",
            paragraphs: [
                "Payments run through Stripe — DriveBai never sees your card number. If a purchase falls through before you accept the car, the hold on your card is released. If you reject a car at inspection, DriveBai support reviews your evidence."
            ]
        ),
    ]
}
