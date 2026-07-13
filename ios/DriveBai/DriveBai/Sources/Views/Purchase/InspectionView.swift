import SwiftUI

/// Screen the buyer opens after the seller confirms keys-handed-over. Two
/// primary CTAs: "Accept vehicle" (green) completes payment; "I do not
/// accept the car" (red) pushes into `RejectionEvidenceFormView`.
///
/// Accepting is gated behind a REQUIRED interactive checklist — every item
/// must be ticked before the green CTA enables. The full checklist is sent to
/// the backend on accept; the server additionally refuses to capture unless a
/// title document is on file and the seller set the BoS title condition.
struct InspectionView: View {
    let purchaseRequest: PurchaseRequest
    let onPurchaseUpdated: (PurchaseRequest) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var isAccepting = false
    @State private var showRejectionForm = false
    @State private var errorMessage: String?
    @State private var now = Date()

    // Loaded so we can display the seller-declared title condition read-only
    // and offer a "View Title" affordance next to the "Title reviewed" item.
    @State private var bos: BillOfSale?
    @State private var showTitlePreview = false

    // Required inspection checklist (spec §22). Every one must be true before
    // "Accept vehicle" enables; the same booleans are sent to the backend.
    @State private var vinMatches = false
    @State private var odometerReviewed = false
    @State private var exteriorOk = false
    @State private var interiorOk = false
    @State private var mechanicalTestDriveOk = false
    @State private var titleReviewed = false
    @State private var keysHandedOver = false
    @State private var buyerUnderstands = false

    private let ticker = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    private var allChecked: Bool {
        vinMatches && odometerReviewed && exteriorOk && interiorOk
            && mechanicalTestDriveOk && titleReviewed && keysHandedOver && buyerUnderstands
    }

    /// The server refuses Accept (TITLE_REQUIRED / INSPECTION_CHECKLIST_INCOMPLETE)
    /// unless the seller has uploaded the title AND declared its condition. When
    /// the loaded BoS already tells us that isn't the case, block Accept on the
    /// client so the buyer isn't sent into a guaranteed round-trip rejection.
    /// Only enforced once the BoS has loaded (nil bos → let the server backstop).
    private var titleBlocksAccept: Bool {
        guard let bos else { return false }
        return bos.titleUploaded != true || bos.titleConditionDisplay == nil
    }

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
            .sheet(isPresented: $showTitlePreview) {
                if let url = bos?.titleDocumentUrl {
                    DocumentPreviewSheet(source: .remoteURL(url, filename: "Vehicle Title"))
                }
            }
            .onReceive(ticker) { now = $0 }
            .task { await loadBoS() }
        }
        // Product-tour host lives inside the inspection sheet; re-inject the
        // shared coordinator because a sheet gets a fresh environment.
        .environmentObject(ProductTourCoordinator.shared)
        .onboardingOverlayHost(ProductTourCoordinator.shared)
        .onAppear { ProductTourCoordinator.shared.handle(.inspectionAvailable) }
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
        VStack(alignment: .leading, spacing: 14) {
            Text("Confirm every item before you accept")
                .font(.subheadline.weight(.semibold))
            Text("Accepting completes your payment, so check each item honestly. All are required.")
                .font(.caption)
                .foregroundColor(.secondary)

            checkRow("VIN matches the Bill of Sale", isOn: $vinMatches)
            checkRow("Odometer / mileage reviewed", isOn: $odometerReviewed)
            checkRow("Exterior condition checked", isOn: $exteriorOk)
            checkRow("Interior condition checked", isOn: $interiorOk)
            checkRow("Mechanical / test drive checked", isOn: $mechanicalTestDriveOk)

            titleReviewRow

            checkRow("Seller handed over keys", isOn: $keysHandedOver)

            Divider().padding(.vertical, 2)

            checkRow("I understand accepting completes my payment", isOn: $buyerUnderstands,
                     emphasized: true)
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

    /// "Title reviewed" — carries the seller-declared title condition read-only
    /// plus a "View Title" affordance when the document is on file.
    private var titleReviewRow: some View {
        VStack(alignment: .leading, spacing: 6) {
            checkRow("Title reviewed", isOn: $titleReviewed,
                     subtitle: titleConditionSubtitle)
            HStack(spacing: 14) {
                if bos?.titleUploaded == true, bos?.titleDocumentUrl != nil {
                    Button {
                        showTitlePreview = true
                    } label: {
                        Label("View Title", systemImage: "doc.text.magnifyingglass")
                            .font(.caption.weight(.semibold))
                    }
                } else if bos != nil {
                    Label("Seller hasn't uploaded the title yet", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption.weight(.medium))
                        .foregroundColor(.orange)
                }
            }
            .padding(.leading, 30)
        }
    }

    private var titleConditionSubtitle: String {
        if let display = bos?.titleConditionDisplay {
            return "Seller-declared title: \(display)"
        }
        return "Seller-declared title: not set yet"
    }

    private var paymentHeldCard: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "lock.shield")
                .font(.title3)
                .foregroundColor(.driveBaiPrimary)
            VStack(alignment: .leading, spacing: 4) {
                Text(PurchaseCopy.paymentHoldHeadline)
                    .font(.subheadline.weight(.semibold))
                Text("Accepting the vehicle completes your payment. Rejecting with valid evidence releases the hold — you won't be charged.")
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
            if titleBlocksAccept {
                Text("The seller still needs to upload the vehicle title and set its condition before you can accept.")
                    .font(.caption)
                    .foregroundColor(.orange)
                    .frame(maxWidth: .infinity, alignment: .center)
            } else if !allChecked {
                Text("Tick every item above to accept the vehicle.")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .frame(maxWidth: .infinity, alignment: .center)
            }
            Button(action: acceptVehicle) {
                HStack(spacing: 8) {
                    if isAccepting { ProgressView().tint(.white).scaleEffect(0.85) }
                    Image(systemName: "checkmark.seal.fill")
                    Text(isAccepting ? "Completing payment…" : "Accept vehicle")
                        .font(.headline)
                }
                .foregroundColor(.white)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 14)
                .background((isAccepting || !allChecked || titleBlocksAccept) ? Color.green.opacity(0.5) : Color.green)
                .cornerRadius(12)
            }
            .disabled(isAccepting || !allChecked || titleBlocksAccept)
            .onboardingTarget(.inspectionCTA)

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

    /// Interactive, tappable checklist row backed by a `Bool` binding.
    private func checkRow(
        _ text: String,
        isOn: Binding<Bool>,
        subtitle: String? = nil,
        emphasized: Bool = false
    ) -> some View {
        Button {
            isOn.wrappedValue.toggle()
        } label: {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: isOn.wrappedValue ? "checkmark.circle.fill" : "circle")
                    .font(.title3)
                    .foregroundColor(isOn.wrappedValue ? .green : .secondary)
                VStack(alignment: .leading, spacing: 2) {
                    Text(text)
                        .font(emphasized ? .subheadline.weight(.semibold) : .subheadline)
                        .foregroundColor(.primary)
                        .multilineTextAlignment(.leading)
                    if let subtitle {
                        Text(subtitle)
                            .font(.caption)
                            .foregroundColor(.secondary)
                            .multilineTextAlignment(.leading)
                    }
                }
                Spacer(minLength: 0)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(isAccepting)
    }

    // MARK: - Actions

    private func loadBoS() async {
        bos = try? await APIClient.shared.getBillOfSale(
            purchaseRequestId: purchaseRequest.id
        ).toDomain()
    }

    private func acceptVehicle() {
        guard !isAccepting, allChecked else { return }
        isAccepting = true
        Task {
            defer { isAccepting = false }
            do {
                let checklist = InspectVehicleAcceptAPIRequest(
                    vinMatches: vinMatches,
                    odometerReviewed: odometerReviewed,
                    exteriorOk: exteriorOk,
                    interiorOk: interiorOk,
                    mechanicalTestDriveOk: mechanicalTestDriveOk,
                    titleReviewed: titleReviewed,
                    keysHandedOver: keysHandedOver,
                    buyerUnderstandsAcceptanceCompletesPayment: buyerUnderstands
                )
                let response = try await APIClient.shared.buyerAcceptVehicle(
                    purchaseRequestId: purchaseRequest.id,
                    checklist: checklist
                )
                onPurchaseUpdated(response.toDomain())
                dismiss()
            } catch let apiError as APIError {
                errorMessage = friendlyAcceptError(apiError)
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }

    /// Maps the server's accept gates to clear buyer-facing copy.
    private func friendlyAcceptError(_ error: APIError) -> String {
        switch error.errorCode {
        case "TITLE_REQUIRED":
            return "The seller hasn't uploaded the title yet. Ask them to upload the vehicle title before you accept."
        case "INSPECTION_CHECKLIST_INCOMPLETE":
            return "The seller hasn't set the title condition on the Bill of Sale yet. Ask them to complete it before you accept."
        default:
            return error.errorDescription ?? "Couldn't complete the acceptance."
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
