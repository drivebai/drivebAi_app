import SwiftUI

/// Multi-step wizard for the Bill of Sale.  Follows the MV-912 layout —
/// vehicle → sale terms → seller info → buyer info → signature → review.
/// Either party (buyer OR seller) may sign first; the "Review" step
/// reports which side is still outstanding.
struct BillOfSaleFlowView: View {
    let purchaseRequest: PurchaseRequest
    let currentUserId: UUID
    let initialBoS: BillOfSale?
    /// Called after each successful mutation so the parent
    /// (ChatViewModel) can refresh its cache.
    let onBoSUpdated: (BillOfSale) -> Void
    let onPurchaseUpdated: (PurchaseRequest) -> Void

    @Environment(\.dismiss) private var dismiss

    @State private var currentStep: BoSStep = .vehicle
    @State private var isSaving = false
    @State private var isSigning = false
    @State private var errorMessage: String?
    @State private var bos: BillOfSale?

    // Editable local state
    @State private var vehicleYear: String = ""
    @State private var vehicleMake: String = ""
    @State private var vehicleModel: String = ""
    @State private var vin: String = ""
    @State private var saleAmountString: String = ""
    @State private var termsConditions: String = ""
    @State private var sellerName: String = ""
    @State private var sellerAddress: String = ""
    @State private var buyerName: String = ""
    @State private var buyerAddress: String = ""

    private var isSeller: Bool { currentUserId == purchaseRequest.sellerId }
    private var isBuyer: Bool { currentUserId == purchaseRequest.buyerId }

    private var currentRoleHasSigned: Bool {
        guard let bos else { return false }
        return isSeller ? bos.sellerHasSigned : bos.buyerHasSigned
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Color(.systemGroupedBackground).ignoresSafeArea()
                VStack(spacing: 0) {
                    progressHeader
                    Divider()
                    ScrollView {
                        VStack(alignment: .leading, spacing: 22) {
                            stepContent
                        }
                        .padding(16)
                        .padding(.bottom, 100)
                    }
                }
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    ctaBar
                }
            }
            .navigationTitle("Bill of Sale")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Close") { dismiss() }
                }
            }
            .alert("Something went wrong", isPresented: Binding(
                get: { errorMessage != nil },
                set: { if !$0 { errorMessage = nil } }
            )) {
                Button("OK", role: .cancel) { errorMessage = nil }
            } message: {
                Text(errorMessage ?? "")
            }
            .onAppear { seedInitialState() }
            // GET the current BoS row so subsequent PATCHes have a
            // populated `bos` state. Without this, first-open bos is
            // nil (ChatViewModel only caches on a successful PATCH),
            // saveEdits() returns early, and every "Save & continue"
            // silently discards the user's typing.
            .task {
                await loadBoS()
            }
        }
    }

    // MARK: - Step content

    @ViewBuilder
    private var stepContent: some View {
        switch currentStep {
        case .vehicle:  vehicleStep
        case .sale:     saleStep
        case .seller:   sellerStep
        case .buyer:    buyerStep
        case .signature: signatureStep
        case .review:   reviewStep
        }
    }

    private var vehicleStep: some View {
        PurchaseSectionCard(title: "Vehicle") {
            VStack(spacing: 10) {
                PurchaseFormField("Year", text: $vehicleYear, keyboardType: .numberPad)
                PurchaseFormField("Make", text: $vehicleMake)
                PurchaseFormField("Model", text: $vehicleModel)
                PurchaseFormField("VIN", text: $vin)
            }
            if bos?.buyerHasSigned == true || bos?.sellerHasSigned == true {
                Text("Vehicle details are locked once either party signs.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
    }

    private var saleStep: some View {
        VStack(spacing: 16) {
            PurchaseSectionCard(title: "Sale amount") {
                PurchaseFormField("Amount (USD)", text: $saleAmountString, keyboardType: .decimalPad)
                Text("Defaults to the accepted offer.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            PurchaseSectionCard(title: "Terms & conditions") {
                ZStack(alignment: .topLeading) {
                    TextEditor(text: $termsConditions)
                        .font(.subheadline)
                        .frame(minHeight: 140)
                        .padding(10)
                        .background(Color(.systemGray6))
                        .cornerRadius(10)
                    if termsConditions.isEmpty {
                        Text("Vehicle is sold as-is, where-is, with no warranties unless otherwise stated in writing.")
                            .font(.subheadline)
                            .foregroundColor(Color(.systemGray3))
                            .padding(14)
                            .allowsHitTesting(false)
                    }
                }
            }
        }
    }

    private var sellerStep: some View {
        PurchaseSectionCard(title: "Seller identity") {
            VStack(spacing: 10) {
                PurchaseFormField("Legal name", text: $sellerName)
                PurchaseFormField("Address", text: $sellerAddress, axis: .vertical)
            }
            if !isSeller {
                Text("Only the seller can edit this section.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
        .disabled(!isSeller)
    }

    private var buyerStep: some View {
        PurchaseSectionCard(title: "Buyer identity") {
            VStack(spacing: 10) {
                PurchaseFormField("Legal name", text: $buyerName)
                PurchaseFormField("Address", text: $buyerAddress, axis: .vertical)
            }
            if !isBuyer {
                Text("Only the buyer can edit this section.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
        .disabled(!isBuyer)
    }

    private var signatureStep: some View {
        VStack(alignment: .leading, spacing: 20) {
            if currentRoleHasSigned {
                Label("Your signature is on file", systemImage: "checkmark.seal.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.green)
                    .padding(14)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.green.opacity(0.08))
                    .cornerRadius(12)

                if let bos, !bos.isFullySigned {
                    Text(bos.sellerHasSigned && !bos.buyerHasSigned
                         ? "Waiting on the buyer's signature."
                         : "Waiting on the seller's signature.")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                }
            } else {
                PurchaseSignaturePadView(
                    title: isSeller ? "Seller signature" : "Buyer signature",
                    subtitle: "Draw your signature below with your finger.",
                    isSaving: isSigning,
                    onSave: { data in
                        Task { await submitSignature(data: data) }
                    }
                )
            }
        }
    }

    private var reviewStep: some View {
        VStack(alignment: .leading, spacing: 16) {
            PurchaseSectionCard(title: "Vehicle") {
                reviewRow("Year", vehicleYear)
                reviewRow("Make", vehicleMake)
                reviewRow("Model", vehicleModel)
                reviewRow("VIN", vin)
            }
            PurchaseSectionCard(title: "Sale") {
                reviewRow("Amount", displaySaleAmount)
                if !termsConditions.isEmpty {
                    Text(termsConditions)
                        .font(.footnote)
                        .foregroundColor(.secondary)
                }
            }
            PurchaseSectionCard(title: "Seller") {
                reviewRow("Name", sellerName)
                reviewRow("Address", sellerAddress)
                reviewSignatureRow(signed: bos?.sellerHasSigned == true, label: "Seller")
            }
            PurchaseSectionCard(title: "Buyer") {
                reviewRow("Name", buyerName)
                reviewRow("Address", buyerAddress)
                reviewSignatureRow(signed: bos?.buyerHasSigned == true, label: "Buyer")
            }

            if let bos, bos.isFullySigned {
                Label("Bill of Sale fully signed — you can now authorize payment.",
                      systemImage: "checkmark.seal.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.green)
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.green.opacity(0.08))
                    .cornerRadius(10)
            } else {
                Label("Awaiting other party's signature.", systemImage: "hourglass")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    // MARK: - CTA bar / progress

    private var progressHeader: some View {
        let total = BoSStep.allCases.count
        let current = currentStep.rawValue + 1
        let pct = CGFloat(current) / CGFloat(total)
        return VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Step \(current) of \(total) — \(currentStep.title)")
                    .font(.caption.weight(.semibold))
                    .foregroundColor(.secondary)
                Spacer()
                if isSaving {
                    HStack(spacing: 5) {
                        ProgressView().scaleEffect(0.75)
                        Text("Saving…").font(.caption).foregroundColor(.secondary)
                    }
                }
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    RoundedRectangle(cornerRadius: 4)
                        .fill(Color.driveBaiPrimary.opacity(0.15))
                        .frame(height: 8)
                    RoundedRectangle(cornerRadius: 4)
                        .fill(Color.driveBaiPrimary)
                        .frame(width: geo.size.width * pct, height: 8)
                        .animation(.easeInOut(duration: 0.28), value: currentStep.rawValue)
                }
            }
            .frame(height: 8)
        }
        .padding(16)
        .background(Color(.systemBackground))
    }

    private var ctaBar: some View {
        HStack(spacing: 12) {
            if currentStep != .vehicle {
                Button("Back") {
                    if let prev = BoSStep(rawValue: currentStep.rawValue - 1) {
                        currentStep = prev
                    }
                }
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.driveBaiPrimary)
                .frame(maxWidth: .infinity, minHeight: 52)
                .overlay(
                    RoundedRectangle(cornerRadius: 14)
                        .stroke(Color.driveBaiPrimary, lineWidth: 1.5)
                )
            }

            if currentStep == .review {
                Button("Close") { dismiss() }
                    .font(.subheadline.weight(.semibold))
                    .frame(maxWidth: .infinity, minHeight: 52)
                    .background(Color.driveBaiPrimary)
                    .foregroundColor(.white)
                    .cornerRadius(14)
            } else {
                Button {
                    Task { await primaryTap() }
                } label: {
                    HStack(spacing: 8) {
                        if isSaving { ProgressView().tint(.white).scaleEffect(0.85) }
                        Text(primaryLabel)
                            .font(.subheadline.weight(.semibold))
                    }
                    .frame(maxWidth: .infinity, minHeight: 52)
                    .background(isSaving ? Color.driveBaiPrimary.opacity(0.6) : Color.driveBaiPrimary)
                    .foregroundColor(.white)
                    .cornerRadius(14)
                }
                .disabled(isSaving)
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 12)
        .padding(.bottom, 10)
        .background(
            Color(.systemBackground)
                .shadow(color: .black.opacity(0.07), radius: 10, x: 0, y: -4)
                .ignoresSafeArea(edges: .bottom)
        )
    }

    private var primaryLabel: String {
        switch currentStep {
        case .vehicle, .sale, .seller, .buyer:
            return "Save & continue"
        case .signature:
            return currentRoleHasSigned ? "Continue" : "Skip for now"
        case .review:
            return "Done"
        }
    }

    // MARK: - Actions

    private func primaryTap() async {
        switch currentStep {
        case .vehicle, .sale, .seller, .buyer:
            await saveEdits()
            advance()
        case .signature:
            advance()
        case .review:
            dismiss()
        }
    }

    private func advance() {
        if let next = BoSStep(rawValue: currentStep.rawValue + 1) {
            currentStep = next
        }
    }

    /// GETs the BoS row on flow appear so `bos` is populated before
    /// the first "Save & continue" tap. Best-effort — a network hiccup
    /// still leaves the wizard usable (the first successful PATCH will
    /// backfill `bos` from the response).
    private func loadBoS() async {
        do {
            let response = try await APIClient.shared.getBillOfSale(
                purchaseRequestId: purchaseRequest.id
            )
            let domain = response.toDomain()
            bos = domain
            onBoSUpdated(domain)
        } catch {
            #if DEBUG
            print("[BillOfSaleFlow] getBillOfSale error: \(error)")
            #endif
        }
    }

    /// PATCH edits from the current step to the backend. Dispatches to
    /// the role-appropriate endpoint — seller-owned fields go to /bos
    /// (403s buyers), buyer identity fields go to /bos/buyer-fields
    /// (403s sellers). Prior version bundled everything into /bos and
    /// silently ignored buyer edits with a 403 that never surfaced.
    private func saveEdits() async {
        isSaving = true
        defer { isSaving = false }

        do {
            let response: BillOfSaleAPIResponse
            if isSeller {
                let body = UpdateBillOfSaleAPIRequest(
                    vehicleYear: Int(vehicleYear),
                    vehicleMake: vehicleMake,
                    vehicleModel: vehicleModel,
                    vin: vin,
                    termsConditions: termsConditions,
                    sellerName: sellerName,
                    sellerAddress: sellerAddress
                )
                response = try await APIClient.shared.updateBillOfSale(
                    purchaseRequestId: purchaseRequest.id,
                    request: body
                )
            } else if isBuyer {
                let body = UpdateBillOfSaleBuyerFieldsAPIRequest(
                    buyerName: buyerName,
                    buyerAddress: buyerAddress
                )
                response = try await APIClient.shared.updateBillOfSaleBuyerFields(
                    purchaseRequestId: purchaseRequest.id,
                    request: body
                )
            } else {
                return
            }
            let domain = response.toDomain()
            bos = domain
            onBoSUpdated(domain)
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func submitSignature(data: Data) async {
        guard !isSigning else { return }
        isSigning = true
        defer { isSigning = false }

        do {
            let response: BillOfSaleAPIResponse
            if isSeller {
                response = try await APIClient.shared.sellerSignBillOfSale(
                    purchaseRequestId: purchaseRequest.id,
                    signatureData: data
                )
            } else {
                response = try await APIClient.shared.buyerSignBillOfSale(
                    purchaseRequestId: purchaseRequest.id,
                    signatureData: data
                )
            }
            let domain = response.toDomain()
            bos = domain
            onBoSUpdated(domain)

            // Also refresh the purchase-request status which may have flipped
            // to bos_pending_* or bos_signed.
            if let updated = try? await APIClient.shared.fetchPurchaseRequest(
                id: purchaseRequest.id
            ) {
                onPurchaseUpdated(updated.toDomain())
            }
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    // MARK: - Helpers

    private var saleAmountCents: Int64 {
        let clean = saleAmountString.replacingOccurrences(of: ",", with: "")
        return Int64((Double(clean) ?? 0) * 100)
    }

    private var displaySaleAmount: String {
        let dollars = Double(saleAmountCents) / 100.0
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = "USD"
        formatter.maximumFractionDigits = 0
        return formatter.string(from: NSNumber(value: dollars)) ?? "$\(Int(dollars))"
    }

    private func seedInitialState() {
        let existing = initialBoS
        bos = existing

        vehicleYear = existing.map { String($0.vehicleYear) } ?? String(purchaseRequest.carTitle.prefix(4))
        vehicleMake = existing?.vehicleMake ?? ""
        vehicleModel = existing?.vehicleModel ?? ""
        vin = existing?.vin ?? ""

        let cents = existing?.saleAmountCents ?? purchaseRequest.offerAmountCents
        saleAmountString = String(format: "%.2f", Double(cents) / 100.0)

        termsConditions = existing?.termsConditions
            ?? "Vehicle is sold as-is, where-is, with no warranties unless otherwise stated in writing."
        sellerName = existing?.sellerName ?? purchaseRequest.sellerName
        sellerAddress = existing?.sellerAddress ?? ""
        buyerName = existing?.buyerName ?? purchaseRequest.buyerName
        buyerAddress = existing?.buyerAddress ?? ""
    }

    private func reviewRow(_ label: String, _ value: String) -> some View {
        HStack(alignment: .top) {
            Text(label)
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(width: 90, alignment: .leading)
            Text(value.isEmpty ? "—" : value)
                .font(.subheadline)
                .foregroundColor(.primary)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func reviewSignatureRow(signed: Bool, label: String) -> some View {
        HStack {
            Image(systemName: signed ? "checkmark.seal.fill" : "circle.dashed")
                .foregroundColor(signed ? .green : .secondary)
            Text(signed ? "\(label) signature on file" : "\(label) has not signed yet")
                .font(.caption.weight(.medium))
                .foregroundColor(signed ? .green : .secondary)
        }
    }
}

// MARK: - Step enum

private enum BoSStep: Int, CaseIterable {
    case vehicle = 0
    case sale
    case seller
    case buyer
    case signature
    case review

    var title: String {
        switch self {
        case .vehicle: return "Vehicle"
        case .sale: return "Sale terms"
        case .seller: return "Seller info"
        case .buyer: return "Buyer info"
        case .signature: return "Signature"
        case .review: return "Review"
        }
    }
}

// MARK: - Shared BoS components
//
// SectionCard/FormField from the accident flow are file-private, so we
// define small purchase-scoped analogues here.  Same visual language,
// nothing fancy.

struct PurchaseSectionCard<Content: View>: View {
    let title: String
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(title)
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.primary)
            content()
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.systemBackground))
        .cornerRadius(14)
        .shadow(color: .black.opacity(0.05), radius: 6, x: 0, y: 2)
    }
}

struct PurchaseFormField: View {
    let label: String
    @Binding var text: String
    var keyboardType: UIKeyboardType = .default
    var axis: Axis = .horizontal

    init(_ label: String, text: Binding<String>, keyboardType: UIKeyboardType = .default, axis: Axis = .horizontal) {
        self.label = label
        self._text = text
        self.keyboardType = keyboardType
        self.axis = axis
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption.weight(.medium))
                .foregroundColor(.secondary)
            TextField(label, text: $text, axis: axis)
                .keyboardType(keyboardType)
                .font(.subheadline)
                .lineLimit(axis == .vertical ? 2...5 : 1...1)
                .padding(.horizontal, 11)
                .padding(.vertical, 9)
                .background(Color(.systemGray6))
                .cornerRadius(9)
        }
    }
}
