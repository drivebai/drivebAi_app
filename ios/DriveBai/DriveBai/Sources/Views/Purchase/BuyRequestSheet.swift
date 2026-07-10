import SwiftUI

/// Modal presented from the car detail screen's "Buy this car" CTA.  Lets
/// the buyer confirm/edit their offer, add a short message to the seller,
/// and submit — the response contains the chat id used to navigate the
/// buyer into the fresh conversation.
struct BuyRequestSheet: View {
    let car: Car
    /// Called with the new chatId after the offer POSTs successfully.
    let onSubmitted: (UUID) -> Void

    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var authStore: AuthStore

    @State private var offerAmount: Double
    @State private var buyerMessage: String = ""
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    init(car: Car, onSubmitted: @escaping (UUID) -> Void) {
        self.car = car
        self.onSubmitted = onSubmitted
        _offerAmount = State(initialValue: car.salePrice?.amount ?? 0)
    }

    private var offerCents: Int64 {
        Int64((offerAmount * 100).rounded())
    }

    /// Offers are free-form negotiation — any strictly-positive amount is
    /// accepted (there is no minimum tied to the listed sale price).
    private var isValid: Bool {
        offerAmount > 0
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    carSummary
                    Divider()
                    offerField
                    Divider()
                    messageField
                    Divider()
                    nextStepsCard
                    Divider()
                    disclaimerCard
                }
                .padding(20)
            }
            .navigationTitle("Buy this car")
            .navigationBarTitleDisplayMode(.inline)
            .onAppear {
                // The purchase intro runs on the car detail before this sheet is
                // presented (its first card spotlights the Buy button, which
                // lives there). Here we only keep the context flag true.
                ProductTourCoordinator.shared.updateContext { $0.carIsForSale = true }
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(isSubmitting)
                }
            }
            .safeAreaInset(edge: .bottom) {
                submitButton
                    .padding(16)
                    .background(Color(.systemBackground).ignoresSafeArea(edges: .bottom))
            }
            .alert("Offer failed", isPresented: Binding(
                get: { errorMessage != nil },
                set: { if !$0 { errorMessage = nil } }
            )) {
                Button("OK") { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
        }
        // A presented sheet occludes the tab-root overlay and gets a fresh
        // environment, so the purchase teach needs its own host here. Without
        // it the card drew behind this sheet and only appeared once the sheet
        // was dismissed — long after the moment it was explaining.
        .environmentObject(ProductTourCoordinator.shared)
        .onboardingOverlayHost(ProductTourCoordinator.shared)
    }

    // MARK: - Sections

    private var carSummary: some View {
        HStack(alignment: .top, spacing: 12) {
            RoundedRectangle(cornerRadius: 12)
                .fill(Color(.systemGray5))
                .frame(width: 92, height: 68)
                .overlay(
                    Group {
                        if let url = car.photoSlots.first(where: { $0.slotType == .coverFront })?.fullImageURL {
                            RemoteImage(url: url, contentMode: .fill)
                        } else {
                            Image(systemName: "car.fill")
                                .font(.system(size: 24))
                                .foregroundColor(.secondary)
                        }
                    }
                )
                .clipShape(RoundedRectangle(cornerRadius: 12))

            VStack(alignment: .leading, spacing: 4) {
                Text(car.displayTitle)
                    .font(.headline)
                    .lineLimit(2)
                if let sale = car.salePrice {
                    Text("Sale price: \(sale.formatted)")
                        .font(.subheadline.weight(.semibold))
                        .foregroundColor(.driveBaiPrimary)
                }
                Text("Seller: \(car.owner.name)")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Spacer(minLength: 0)
        }
    }

    private var offerField: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Your offer")
                .font(.subheadline.weight(.semibold))

            HStack(spacing: 8) {
                Text("$")
                    .font(.title2.weight(.semibold))
                    .foregroundColor(.secondary)
                TextField("Offer amount", value: $offerAmount, format: .number)
                    .keyboardType(.decimalPad)
                    .font(.title2.weight(.semibold))
                    .textFieldStyle(.plain)
            }
            .padding(12)
            .background(Color(.systemGray6))
            .cornerRadius(10)

            Text("Enter your offer")
                .font(.caption)
                .foregroundColor(.secondary)
        }
    }

    private var messageField: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Message to seller (optional)")
                .font(.subheadline.weight(.semibold))

            ZStack(alignment: .topLeading) {
                TextEditor(text: $buyerMessage)
                    .font(.subheadline)
                    .frame(minHeight: 90)
                    .padding(10)
                    .background(Color(.systemGray6))
                    .cornerRadius(10)
                if buyerMessage.isEmpty {
                    Text("Tell the seller a bit about yourself or your intended use.")
                        .font(.subheadline)
                        .foregroundColor(Color(.systemGray3))
                        .padding(14)
                        .allowsHitTesting(false)
                }
            }
        }
    }

    private var nextStepsCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("Here's what happens next", systemImage: "list.bullet.rectangle")
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.driveBaiPrimary)
            VStack(alignment: .leading, spacing: 6) {
                stepRow(number: 1, text: "Seller reviews and accepts your offer.")
                stepRow(number: 2, text: "You both sign a Vehicle Bill of Sale in the app.")
                stepRow(number: 3, text: "You authorize payment — the amount is held on your card, not charged yet.")
                stepRow(number: 4, text: "You meet the seller, inspect the vehicle, and accept it.")
                stepRow(number: 5, text: "Your payment completes when you accept the vehicle.")
            }
            .font(.subheadline)
            .foregroundColor(.primary)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.driveBaiPrimary.opacity(0.08))
        .cornerRadius(12)
    }

    private func stepRow(number: Int, text: String) -> some View {
        HStack(alignment: .top, spacing: 10) {
            Text("\(number).")
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 18, alignment: .leading)
            Text(text)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var disclaimerCard: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(PurchaseCopy.paymentHoldHeadline, systemImage: "lock.shield")
                .font(.caption.weight(.semibold))
                .foregroundColor(.primary)
            Text("DrivaBai is not a licensed escrow agent. Title transfer and DMV paperwork are the responsibility of the buyer and seller — requirements vary by state.")
                .font(.caption)
                .foregroundColor(.secondary)
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.systemGray6))
        .cornerRadius(12)
    }

    private var submitButton: some View {
        Button(action: submitOffer) {
            HStack(spacing: 8) {
                if isSubmitting { ProgressView().tint(.white).scaleEffect(0.85) }
                Text(isSubmitting ? "Sending offer…" : "Send offer")
                    .font(.headline)
            }
            .foregroundColor(.white)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 14)
            .background(isValid && !isSubmitting ? Color.driveBaiPrimary : Color(.systemGray4))
            .cornerRadius(12)
        }
        .disabled(!isValid || isSubmitting)
    }

    // MARK: - Actions

    private func submitOffer() {
        guard authStore.state.user != nil, !isSubmitting else { return }
        isSubmitting = true
        Task {
            defer { isSubmitting = false }
            do {
                let response = try await APIClient.shared.createPurchaseRequest(
                    carId: car.id,
                    offerAmountCents: offerCents,
                    buyerMessage: buyerMessage.isEmpty ? nil : buyerMessage
                )
                dismiss()
                onSubmitted(response.chatId)
            } catch let apiError as APIError {
                errorMessage = apiError.errorDescription
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }
}
