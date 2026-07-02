import SwiftUI
import StripePaymentSheet

/// Presented once the Bill of Sale is fully signed.  The buyer picks how
/// to pay:
///
/// * "I have the funds available" — creates a manual-capture PaymentIntent
///   and hands off to Stripe's PaymentSheet.
/// * "I need financing" — disabled with a "Coming soon" subtitle.  We do
///   not currently partner with any lenders.
struct ChoosePaymentOptionView: View {
    let purchaseRequest: PurchaseRequest
    let onPurchaseUpdated: (PurchaseRequest) -> Void
    let onDismiss: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var isCreatingIntent = false
    @State private var errorMessage: String?
    @State private var paymentIntentResponse: PaymentIntentAPIResponse?
    @State private var showPaymentSheet = false
    @State private var isSyncing = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    headerCard
                    fundsAvailableRow
                    financingRow
                    disclaimerCard
                }
                .padding(20)
            }
            .navigationTitle("Authorize payment")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") {
                        onDismiss()
                        dismiss()
                    }
                    .disabled(isCreatingIntent)
                }
            }
            .alert("Payment error", isPresented: Binding(
                get: { errorMessage != nil },
                set: { if !$0 { errorMessage = nil } }
            )) {
                Button("OK") { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
            .background {
                if showPaymentSheet,
                   let response = paymentIntentResponse,
                   let customerId = response.customerId,
                   let ephemeralKey = response.ephemeralKeySecret {
                    PaymentSheetPresenter(
                        clientSecret: response.paymentIntentClientSecret,
                        ephemeralKeySecret: ephemeralKey,
                        customerId: customerId,
                        publishableKey: response.publishableKey
                    ) { result in
                        showPaymentSheet = false
                        handlePaymentResult(result)
                    }
                    .frame(width: 0, height: 0)
                }
            }
        }
    }

    // MARK: - Sections

    private var headerCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Sale total")
                .font(.caption)
                .foregroundColor(.secondary)
            Text(purchaseRequest.formattedOfferAmount)
                .font(.system(size: 34, weight: .bold, design: .rounded))
                .foregroundColor(.driveBaiPrimary)
            Text(purchaseRequest.carTitle)
                .font(.subheadline.weight(.semibold))
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(18)
        .background(Color.driveBaiPrimary.opacity(0.08))
        .cornerRadius(14)
    }

    private var fundsAvailableRow: some View {
        Button(action: authorizePayment) {
            paymentRow(
                icon: "creditcard.fill",
                title: "I have the funds available",
                subtitle: "Pay with a card or bank via Stripe. Funds are held, not captured.",
                accent: true,
                isBusy: isCreatingIntent
            )
        }
        .buttonStyle(.plain)
        .disabled(isCreatingIntent)
    }

    private var financingRow: some View {
        paymentRow(
            icon: "hourglass",
            title: "I need financing",
            subtitle: "Coming soon — DrivaBai does not currently offer financing.",
            accent: false,
            isDisabled: true
        )
    }

    private var disclaimerCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label(PurchaseCopy.paymentHoldHeadline, systemImage: "lock.shield")
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.primary)
            Text(PurchaseCopy.paymentHoldDetail)
                .font(.footnote)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.systemGray6))
        .cornerRadius(12)
    }

    private func paymentRow(
        icon: String,
        title: String,
        subtitle: String,
        accent: Bool,
        isDisabled: Bool = false,
        isBusy: Bool = false
    ) -> some View {
        HStack(spacing: 14) {
            Image(systemName: icon)
                .font(.title3)
                .foregroundColor(accent ? Color.driveBaiPrimary : Color(.systemGray))
                .frame(width: 44, height: 44)
                .background((accent ? Color.driveBaiPrimary : Color(.systemGray)).opacity(0.12))
                .clipShape(RoundedRectangle(cornerRadius: 12))

            VStack(alignment: .leading, spacing: 4) {
                Text(title)
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(isDisabled ? .secondary : .primary)
                Text(subtitle)
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            Spacer(minLength: 8)

            if isBusy {
                ProgressView().scaleEffect(0.9)
            } else if !isDisabled {
                Image(systemName: "chevron.right")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.secondary)
            }
        }
        .padding(14)
        .background(Color(.systemBackground))
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .stroke(accent ? Color.driveBaiPrimary : Color(.systemGray4), lineWidth: 1)
        )
        .cornerRadius(14)
        .opacity(isDisabled ? 0.7 : 1)
    }

    // MARK: - Actions

    private func authorizePayment() {
        guard !isCreatingIntent else { return }
        isCreatingIntent = true
        Task {
            defer { isCreatingIntent = false }
            do {
                let response = try await APIClient.shared.createPurchasePaymentIntent(
                    purchaseRequestId: purchaseRequest.id
                )
                paymentIntentResponse = response
                showPaymentSheet = true
            } catch let apiError as APIError {
                errorMessage = apiError.errorDescription
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }

    private func handlePaymentResult(_ result: PaymentSheetResult) {
        switch result {
        case .completed:
            Task {
                isSyncing = true
                defer { isSyncing = false }
                // Ask the backend to reconcile with Stripe immediately —
                // the webhook usually beats this, but the sync endpoint
                // gives snappy feedback.
                if let response = try? await APIClient.shared.syncPurchasePayment(
                    purchaseRequestId: purchaseRequest.id
                ) {
                    onPurchaseUpdated(response.toDomain())
                }
                dismiss()
                onDismiss()
            }
        case .canceled:
            paymentIntentResponse = nil
        case .failed(let error):
            errorMessage = "Payment failed: \(error.localizedDescription)"
            paymentIntentResponse = nil
        }
    }
}
