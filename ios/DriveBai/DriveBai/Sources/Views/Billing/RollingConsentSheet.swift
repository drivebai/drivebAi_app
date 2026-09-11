import SwiftUI

/// The authorization screen for a weekly recurring rental.
///
/// This screen exists to produce evidence, so it has one hard rule: the
/// text it shows is the text the SERVER recorded on the consent row, passed
/// in verbatim from the payment-intent response. It is never a local copy
/// of that wording — a local copy could drift from the stored row, and then
/// the record of what the driver agreed to would prove nothing.
///
/// The mandate is not created here. Confirming the payment that follows is
/// what activates it server-side; dismissing this screen charges nothing.
struct RollingConsentSheet: View {
    /// Exact text the server recorded (`disclosure_text`). Non-optional by
    /// construction: there is no version of this screen without it.
    let disclosureText: String
    /// The version string that text was recorded under (`terms_version`).
    let termsVersion: String?
    /// First charge, in cents — the amount the button is about to take.
    let amountCents: Int64
    let currencyCode: String
    let carTitle: String
    let onAuthorize: () -> Void
    let onCancel: () -> Void

    @State private var didAuthorize = false
    @Environment(\.dismiss) private var dismiss

    private static let termsURL = URL(string: "https://drivebai-landing-v2.netlify.app/terms")!

    private var formattedAmount: String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currencyCode
        let value = Double(amountCents) / 100
        return formatter.string(from: NSNumber(value: value)) ?? String(format: "$%.2f", value)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    header
                    chargeSummary
                    recordedDisclosure
                    authorizationToggle
                    termsFooter
                }
                .padding(20)
            }
            .background(Color(.systemGroupedBackground))
            .safeAreaInset(edge: .bottom) { actionBar }
            .navigationTitle("Weekly rental")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { onCancel() }
                }
            }
            .interactiveDismissDisabled(false)
        }
    }

    // MARK: - Sections

    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(carTitle)
                .font(.headline)
            Text("No fixed end date. It renews every 7 days until you return the car or turn off auto-renew.")
                .font(.subheadline)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// The first charge stated on its own, separately from the recurring
    /// one — a driver should never have to infer today's amount from a
    /// paragraph about future weeks.
    private var chargeSummary: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                Text("Charged now")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                Spacer()
                Text(formattedAmount)
                    .font(.title2.weight(.bold))
                    .monospacedDigit()
            }
            Divider()
            HStack(alignment: .firstTextBaseline) {
                Text("Then every 7 days")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                Spacer()
                Text(formattedAmount)
                    .font(.headline)
                    .monospacedDigit()
            }
        }
        .padding(16)
        .background(Color(.secondarySystemGroupedBackground))
        .clipShape(RoundedRectangle(cornerRadius: 14))
    }

    /// Always visible, never behind a disclosure triangle or a "read more":
    /// the driver has to be able to see what they are authorizing without
    /// taking an extra action to reveal it.
    private var recordedDisclosure: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("What you're authorizing")
                .font(.subheadline.weight(.semibold))
            Text(disclosureText)
                .font(.footnote)
                .foregroundColor(.primary)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemGroupedBackground))
                .overlay(
                    RoundedRectangle(cornerRadius: 12)
                        .stroke(Color(.systemGray4), lineWidth: 1)
                )
                .clipShape(RoundedRectangle(cornerRadius: 12))
        }
    }

    private var authorizationToggle: some View {
        Button {
            didAuthorize.toggle()
        } label: {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: didAuthorize ? "checkmark.square.fill" : "square")
                    .font(.system(size: 22))
                    .foregroundColor(didAuthorize ? Color.driveBaiPrimary : .secondary)
                Text("I authorize these weekly charges.")
                    .font(.subheadline)
                    .foregroundColor(.primary)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(.secondarySystemGroupedBackground))
            .clipShape(RoundedRectangle(cornerRadius: 12))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(didAuthorize ? [.isSelected, .isButton] : .isButton)
    }

    private var termsFooter: some View {
        VStack(alignment: .leading, spacing: 6) {
            Link("DriveBai Terms of Service", destination: Self.termsURL)
                .font(.footnote)
            if let termsVersion, !termsVersion.isEmpty {
                Text("Agreement version: \(termsVersion)")
                    .font(.caption2)
                    .foregroundColor(.secondary)
                    .textSelection(.enabled)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var actionBar: some View {
        VStack(spacing: 8) {
            Button(action: onAuthorize) {
                Text("Pay \(formattedAmount) and start rental")
                    .font(.headline)
                    .foregroundColor(.white)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 14)
                    .background(didAuthorize ? Color.driveBaiPrimary : Color(.systemGray3))
                    .clipShape(RoundedRectangle(cornerRadius: 12))
            }
            .disabled(!didAuthorize)

            Text("Returning the car in the app stops the charges — any day, no notice.")
                .font(.caption)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.horizontal, 20)
        .padding(.top, 12)
        .padding(.bottom, 8)
        .background(.bar)
    }
}


// MARK: - Owner terms

/// What an owner is prompted with when they accept a WEEKLY rental request
/// for the first time. Same rule as the driver's sheet: the text is the
/// server's, fetched and shown verbatim, and the version the server named is
/// the version the acceptance is recorded under. Recording happens in
/// `onAgree` (the caller posts the acceptance and retries the accept).
struct OwnerTermsSheet: View {
    let onAgree: (_ termsVersion: String) async -> Void
    let onCancel: () -> Void

    @State private var terms: OwnerTermsAPIResponse?
    @State private var loadError: String?
    @State private var didAgree = false
    @State private var isSubmitting = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Weekly rentals — owner terms")
                            .font(.headline)
                        Text("A weekly rental renews every 7 days from the driver's saved card. Before you accept one, read how you're paid, what happens if a driver's payment fails, and what DriveBai does and doesn't cover.")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    if let terms {
                        Text(terms.termsText)
                            .font(.footnote)
                            .foregroundColor(.primary)
                            .fixedSize(horizontal: false, vertical: true)
                            .textSelection(.enabled)
                            .padding(14)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Color(.secondarySystemGroupedBackground))
                            .overlay(RoundedRectangle(cornerRadius: 12).stroke(Color(.systemGray4), lineWidth: 1))
                            .clipShape(RoundedRectangle(cornerRadius: 12))
                        Button {
                            didAgree.toggle()
                        } label: {
                            HStack(alignment: .top, spacing: 12) {
                                Image(systemName: didAgree ? "checkmark.square.fill" : "square")
                                    .font(.system(size: 22))
                                    .foregroundColor(didAgree ? Color.driveBaiPrimary : .secondary)
                                Text("I've read these terms and agree to them for my weekly rentals.")
                                    .font(.subheadline)
                                    .foregroundColor(.primary)
                                    .fixedSize(horizontal: false, vertical: true)
                                Spacer(minLength: 0)
                            }
                            .padding(14)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Color(.secondarySystemGroupedBackground))
                            .clipShape(RoundedRectangle(cornerRadius: 12))
                        }
                        .buttonStyle(.plain)
                        Text("Agreement version: \(terms.termsVersion)")
                            .font(.caption2)
                            .foregroundColor(.secondary)
                            .textSelection(.enabled)
                    } else if let loadError {
                        Text(loadError).font(.footnote).foregroundColor(.red)
                    } else {
                        ProgressView("Loading terms…")
                            .frame(maxWidth: .infinity)
                    }
                }
                .padding(20)
            }
            .background(Color(.systemGroupedBackground))
            .safeAreaInset(edge: .bottom) {
                VStack(spacing: 8) {
                    Button {
                        guard let terms, didAgree, !isSubmitting else { return }
                        isSubmitting = true
                        Task {
                            await onAgree(terms.termsVersion)
                            isSubmitting = false
                        }
                    } label: {
                        HStack(spacing: 8) {
                            if isSubmitting { ProgressView().tint(.white).scaleEffect(0.85) }
                            Text(isSubmitting ? "Recording…" : "Agree and accept the request")
                                .font(.headline)
                        }
                        .foregroundColor(.white)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 14)
                        .background((didAgree && terms != nil && !isSubmitting) ? Color.driveBaiPrimary : Color(.systemGray3))
                        .clipShape(RoundedRectangle(cornerRadius: 12))
                    }
                    .disabled(!didAgree || terms == nil || isSubmitting)
                    Text("Nothing is charged to anyone here. Accepting the request is what starts the driver's payment.")
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .padding(.horizontal, 20)
                .padding(.top, 12)
                .padding(.bottom, 8)
                .background(.bar)
            }
            .navigationTitle("Owner terms")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { onCancel() }.disabled(isSubmitting)
                }
            }
            .task {
                do {
                    terms = try await APIClient.shared.fetchOwnerTerms()
                } catch {
                    loadError = "Couldn't load the terms: \(error.localizedDescription)"
                }
            }
        }
    }
}

/// The pending accept an owner-terms sheet was raised for.
struct OwnerTermsPrompt: Identifiable, Equatable {
    let id: UUID
    /// The lease request the owner tried to accept.
    let leaseRequestId: UUID
}
