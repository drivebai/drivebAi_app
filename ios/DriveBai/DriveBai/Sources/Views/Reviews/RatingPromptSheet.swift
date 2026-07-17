import SwiftUI

/// Post-transaction rating sheet: 1–5 stars for the vehicle (buyer/driver
/// only — the server also enforces this) and for the counterparty, plus an
/// optional comment. Presented from the completed Purchase / Vehicle-return
/// Today cards.
struct RatingPromptSheet: View {
    /// "purchase" | "rental" — mirrors the backend transaction types.
    let transactionType: String
    /// purchase_requests.id or vehicle_returns.id respectively.
    let transactionId: UUID
    /// Whether the viewer is the consumer side (buyer/driver) and may rate
    /// the vehicle itself.
    let canRateCar: Bool
    let carTitle: String
    let partnerName: String
    /// Called after a successful submit — and after a 409 (already rated),
    /// which the card treats the same way: stop offering the prompt.
    let onFinished: () -> Void

    @Environment(\.dismiss) private var dismiss

    @State private var carStars = 0
    @State private var partnerStars = 0
    @State private var comment = ""
    @State private var submitting = false
    @State private var errorMessage: String?

    private var canSubmit: Bool {
        !submitting && (carStars > 0 || partnerStars > 0)
    }

    var body: some View {
        NavigationStack {
            Form {
                if canRateCar {
                    Section("Vehicle") {
                        StarPickerRow(title: carTitle, stars: $carStars)
                    }
                }
                Section(canRateCar ? "Your \(transactionType == "purchase" ? "seller" : "host")" : "Your \(transactionType == "purchase" ? "buyer" : "driver")") {
                    StarPickerRow(title: partnerName, stars: $partnerStars)
                }
                Section("Comment (optional)") {
                    TextField("Share how it went…", text: $comment, axis: .vertical)
                        .lineLimit(3...6)
                }
                if let errorMessage {
                    Section {
                        Text(errorMessage)
                            .font(.footnote)
                            .foregroundColor(.red)
                    }
                }
            }
            .navigationTitle("Rate your experience")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Not now") { dismiss() }
                        .disabled(submitting)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(submitting ? "Sending…" : "Submit") { Task { await submit() } }
                        .disabled(!canSubmit)
                }
            }
            .interactiveDismissDisabled(submitting)
        }
    }

    @MainActor
    private func submit() async {
        submitting = true
        errorMessage = nil
        defer { submitting = false }

        let trimmed = comment.trimmingCharacters(in: .whitespacesAndNewlines)
        let request = SubmitReviewRequest(
            transactionType: transactionType,
            transactionId: transactionId,
            carRating: canRateCar && carStars > 0 ? carStars : nil,
            partnerRating: partnerStars > 0 ? partnerStars : nil,
            comment: trimmed.isEmpty ? nil : trimmed
        )

        do {
            _ = try await APIClient.shared.submitReview(request)
            onFinished()
            dismiss()
        } catch let APIError.serverError(code, _) where code == "REVIEW_ALREADY_EXISTS" {
            // Someone (this device, another device) already rated — treat
            // as done rather than trapping the user in an error loop.
            onFinished()
            dismiss()
        } catch {
            errorMessage = "Couldn't send your rating. Please check your connection and try again."
        }
    }
}

/// One tappable 1–5 star row.
struct StarPickerRow: View {
    let title: String
    @Binding var stars: Int

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .font(.subheadline)
                .foregroundColor(.primary)
            HStack(spacing: 10) {
                ForEach(1...5, id: \.self) { i in
                    Image(systemName: i <= stars ? "star.fill" : "star")
                        .font(.title2)
                        .foregroundColor(i <= stars ? .yellow : .secondary)
                        .onTapGesture { stars = (stars == i) ? 0 : i }
                        .accessibilityLabel("\(i) star\(i == 1 ? "" : "s")")
                }
                Spacer()
            }
        }
        .padding(.vertical, 2)
    }
}
