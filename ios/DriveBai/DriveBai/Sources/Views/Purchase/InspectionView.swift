import SwiftUI

/// Screen the buyer opens after the seller confirms keys-handed-over. Two
/// primary CTAs: "Accept vehicle" (green) captures payment; "I do not
/// accept the car" (red) pushes into `RejectionEvidenceFormView`.
struct InspectionView: View {
    let purchaseRequest: PurchaseRequest
    let onPurchaseUpdated: (PurchaseRequest) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var isAccepting = false
    @State private var showRejectionForm = false
    @State private var errorMessage: String?
    @State private var now = Date()

    private let ticker = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    headerCard
                    countdownCard
                    checklistCard
                    paymentHeldCard
                }
                .padding(20)
            }
            .safeAreaInset(edge: .bottom) {
                ctaBar
                    .padding(16)
                    .background(Color(.systemBackground).ignoresSafeArea(edges: .bottom))
            }
            .navigationTitle("Inspect the car")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") { dismiss() }
                        .disabled(isAccepting)
                }
            }
            .alert("Something went wrong", isPresented: Binding(
                get: { errorMessage != nil },
                set: { if !$0 { errorMessage = nil } }
            )) {
                Button("OK") { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
            .navigationDestination(isPresented: $showRejectionForm) {
                RejectionEvidenceFormView(
                    purchaseRequest: purchaseRequest,
                    onRejectionSubmitted: { updated in
                        onPurchaseUpdated(updated)
                        dismiss()
                    }
                )
            }
            .onReceive(ticker) { now = $0 }
        }
    }

    // MARK: - Sections

    private var headerCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(purchaseRequest.carTitle)
                .font(.title3.bold())
            Text("Confirmed sale total: \(purchaseRequest.formattedOfferAmount)")
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.driveBaiPrimary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(Color.driveBaiPrimary.opacity(0.08))
        .cornerRadius(14)
    }

    @ViewBuilder
    private var countdownCard: some View {
        if let deadline = purchaseRequest.inspectionDeadlineAt,
           let remaining = purchaseRequest.inspectionTimeRemaining(now: now) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 6) {
                    Image(systemName: "clock.fill")
                        .foregroundColor(.orange)
                    Text("Time to inspect")
                        .font(.subheadline.weight(.semibold))
                }
                Text(remaining > 0 ? formatRemaining(remaining) : "Deadline passed")
                    .font(.title2.bold())
                    .foregroundColor(remaining > 0 ? .primary : .red)
                Text("Deadline: \(deadline.formatted(date: .abbreviated, time: .shortened))")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
            .background(Color(.systemGray6))
            .cornerRadius(12)
        }
    }

    private var checklistCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Before you accept, verify:")
                .font(.subheadline.weight(.semibold))
            checklistRow("VIN matches the Bill of Sale")
            checklistRow("Keys, spare, and remote received")
            checklistRow("Title and paperwork handed over")
            checklistRow("Vehicle condition matches description")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(Color(.systemBackground))
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .stroke(Color(.systemGray5), lineWidth: 1)
        )
        .cornerRadius(14)
    }

    private var paymentHeldCard: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "lock.shield")
                .font(.title3)
                .foregroundColor(.driveBaiPrimary)
            VStack(alignment: .leading, spacing: 4) {
                Text(PurchaseCopy.paymentHoldHeadline)
                    .font(.subheadline.weight(.semibold))
                Text("Accepting the vehicle triggers the capture. Rejecting with valid evidence releases the hold.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
        .padding(14)
        .background(Color.driveBaiPrimary.opacity(0.06))
        .cornerRadius(12)
    }

    private var ctaBar: some View {
        VStack(spacing: 10) {
            Button(action: acceptVehicle) {
                HStack(spacing: 8) {
                    if isAccepting { ProgressView().tint(.white).scaleEffect(0.85) }
                    Image(systemName: "checkmark.seal.fill")
                    Text(isAccepting ? "Capturing payment…" : "Accept vehicle")
                        .font(.headline)
                }
                .foregroundColor(.white)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 14)
                .background(Color.green)
                .cornerRadius(12)
            }
            .disabled(isAccepting)

            Button {
                showRejectionForm = true
            } label: {
                Text("I do not accept the car")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.red)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 12)
                    .background(Color.red.opacity(0.1))
                    .cornerRadius(12)
            }
            .disabled(isAccepting)
        }
    }

    private func checklistRow(_ text: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "checkmark.circle")
                .foregroundColor(.driveBaiPrimary)
            Text(text)
                .font(.subheadline)
                .foregroundColor(.primary)
        }
    }

    // MARK: - Actions

    private func acceptVehicle() {
        guard !isAccepting else { return }
        isAccepting = true
        Task {
            defer { isAccepting = false }
            do {
                let response = try await APIClient.shared.buyerAcceptVehicle(
                    purchaseRequestId: purchaseRequest.id
                )
                onPurchaseUpdated(response.toDomain())
                dismiss()
            } catch let apiError as APIError {
                errorMessage = apiError.errorDescription
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }

    private func formatRemaining(_ seconds: TimeInterval) -> String {
        let total = Int(seconds)
        let hours = total / 3600
        let minutes = (total % 3600) / 60
        let secs = total % 60
        if hours > 0 {
            return String(format: "%dh %02dm %02ds", hours, minutes, secs)
        }
        return String(format: "%dm %02ds", minutes, secs)
    }
}
