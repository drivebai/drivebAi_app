import SwiftUI

/// Multi-step wizard for the Bill of Sale.  Follows the MV-912 layout —
/// vehicle → sale terms → seller info → buyer info → signature → review.
/// Either party (buyer OR seller) may sign first; the "Review" step
/// reports which side is still outstanding.
///
/// This view intentionally does NOT own its BoS cache. Callers pass an
/// optional `initialBoS` (from ChatViewModel.billOfSalesByPurchase) plus
/// an `onBoSUpdated` callback so the parent VM stays authoritative. On
/// appear the view still runs a GET so the wizard has a fully-populated
/// row even if the cache was cold.
struct BillOfSaleFlowView: View {
    let purchaseRequest: PurchaseRequest
    let currentUserId: UUID
    let initialBoS: BillOfSale?
    /// Optional starting step. Card-driven callers (ChatView) hint the
    /// next incomplete step so the user doesn't have to click through
    /// already-completed sections.
    var initialStep: BoSStep? = nil
    /// Called after each successful mutation so the parent
    /// (ChatViewModel) can refresh its cache.
    let onBoSUpdated: (BillOfSale) -> Void
    let onPurchaseUpdated: (PurchaseRequest) -> Void
    /// Optional stream from ChatViewModel so a WS-driven counterparty
    /// update refreshes the sheet without a manual reload. When nil the
    /// wizard is fully self-driven (fine for previews / detached uses).
    var externalBoSStream: BillOfSale? = nil

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

    /// Per-step dirty flags. Only steps in this set produce a PATCH on
    /// the next "Save & continue" tap.
    @State private var dirty: Set<BoSStep> = []
    /// Last-known-server BoS the local fields were rehydrated from. Used
    /// so field-vs-server diffing survives across refetches.
    @State private var lastServer: BillOfSale?
    /// True once we've applied at least one seed (either from initialBoS
    /// on appear or the first GET). Lets us gate downstream rehydrates on
    /// "did we run yet" instead of just "is bos nil".
    @State private var hasSeeded: Bool = false
    /// Set when a WS update lands while the user has unsaved edits.
    /// The banner offers a manual reload that drops those edits.
    @State private var showStaleBanner: Bool = false
    /// True while `rehydrateFields(from:)` is programmatically writing
    /// @State from a server snapshot. Prevents the `.onChange` handlers
    /// from marking those steps dirty. Everything outside this window
    /// (user typing, in particular during a cold-start GET) counts.
    @State private var suppressDirtyTracking: Bool = false

    private var isSeller: Bool { currentUserId == purchaseRequest.sellerId }
    private var isBuyer: Bool { currentUserId == purchaseRequest.buyerId }

    private var currentRoleHasSigned: Bool {
        guard let bos else { return false }
        return isSeller ? bos.sellerHasSigned : bos.buyerHasSigned
    }

    private var otherRoleHasSigned: Bool {
        guard let bos else { return false }
        return isSeller ? bos.buyerHasSigned : bos.sellerHasSigned
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Color(.systemGroupedBackground).ignoresSafeArea()
                VStack(spacing: 0) {
                    progressHeader
                    if showStaleBanner { staleBanner }
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
            .task {
                await loadBoS()
            }
            .onChange(of: externalBoSStream) { _, fresh in
                guard let fresh, fresh.purchaseRequestId == purchaseRequest.id else { return }
                mergeExternalUpdate(fresh)
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
                    .onChange(of: vehicleYear) { _, _ in markDirty(.vehicle) }
                PurchaseFormField("Make", text: $vehicleMake)
                    .onChange(of: vehicleMake) { _, _ in markDirty(.vehicle) }
                PurchaseFormField("Model", text: $vehicleModel)
                    .onChange(of: vehicleModel) { _, _ in markDirty(.vehicle) }
                PurchaseFormField("VIN", text: $vin)
                    .onChange(of: vin) { _, _ in markDirty(.vehicle) }
            }
            vehicleLockCaption
        }
        .disabled(!isSeller || sellerLockedForEdits)
    }

    @ViewBuilder
    private var vehicleLockCaption: some View {
        if !isSeller {
            Text("The seller fills in vehicle details. This section is read-only for you.")
                .font(.caption)
                .foregroundColor(.secondary)
        } else if sellerLockedForEdits {
            Text("Vehicle details are locked because you have signed the Bill of Sale.")
                .font(.caption)
                .foregroundColor(.secondary)
        }
    }

    private var saleStep: some View {
        VStack(spacing: 16) {
            PurchaseSectionCard(title: "Sale amount") {
                PurchaseFormField("Amount (USD)", text: $saleAmountString, keyboardType: .decimalPad)
                Text("Locked to the accepted offer.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .disabled(true) // sale amount is not user-editable per spec §6 item 5
            PurchaseSectionCard(title: "Terms & conditions") {
                ZStack(alignment: .topLeading) {
                    TextEditor(text: $termsConditions)
                        .font(.subheadline)
                        .frame(minHeight: 140)
                        .padding(10)
                        .background(Color(.systemGray6))
                        .cornerRadius(10)
                        .onChange(of: termsConditions) { _, _ in markDirty(.sale) }
                    if termsConditions.isEmpty {
                        Text("Vehicle is sold as-is, where-is, with no warranties unless otherwise stated in writing.")
                            .font(.subheadline)
                            .foregroundColor(Color(.systemGray3))
                            .padding(14)
                            .allowsHitTesting(false)
                    }
                }
                saleLockCaption
            }
            .disabled(!isSeller || sellerLockedForEdits)
        }
    }

    @ViewBuilder
    private var saleLockCaption: some View {
        if !isSeller {
            Text("Only the seller edits terms & conditions.")
                .font(.caption)
                .foregroundColor(.secondary)
        } else if sellerLockedForEdits {
            Text("Terms are locked because you have signed the Bill of Sale.")
                .font(.caption)
                .foregroundColor(.secondary)
        }
    }

    private var sellerStep: some View {
        PurchaseSectionCard(title: "Seller identity") {
            VStack(spacing: 10) {
                PurchaseFormField("Legal name", text: $sellerName)
                    .onChange(of: sellerName) { _, _ in markDirty(.seller) }
                PurchaseFormField("Address", text: $sellerAddress, axis: .vertical)
                    .onChange(of: sellerAddress) { _, _ in markDirty(.seller) }
            }
            if !isSeller {
                Text("Only the seller can edit this section.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            } else if sellerLockedForEdits {
                Text("Locked because you have signed.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
        .disabled(!isSeller || sellerLockedForEdits)
    }

    private var buyerStep: some View {
        PurchaseSectionCard(title: "Buyer identity") {
            VStack(spacing: 10) {
                PurchaseFormField("Legal name", text: $buyerName)
                    .onChange(of: buyerName) { _, _ in markDirty(.buyer) }
                PurchaseFormField("Address", text: $buyerAddress, axis: .vertical)
                    .onChange(of: buyerAddress) { _, _ in markDirty(.buyer) }
            }
            if !isBuyer {
                Text("Only the buyer can edit this section.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            } else if buyerLockedForEdits {
                Text("Locked because you have signed.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
        .disabled(!isBuyer || buyerLockedForEdits)
    }

    private var signatureStep: some View {
        VStack(alignment: .leading, spacing: 20) {
            Text(isSeller ? "You are signing as the seller." : "You are signing as the buyer.")
                .font(.subheadline.weight(.medium))
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)

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
            } else if dirty.contains(where: { $0 != .signature && $0 != .review }) {
                // Guard: force the user to save pending edits before signing
                // so their in-flight typing isn't captured mid-flight by the
                // signed snapshot.
                Text("Save your pending changes before signing.")
                    .font(.subheadline)
                    .foregroundColor(.orange)
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.orange.opacity(0.08))
                    .cornerRadius(10)
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
                reviewRow("Terms", termsConditions.isEmpty ? "—" : termsConditions)
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
                Label("Bill of Sale fully signed — the buyer can now authorize payment.",
                      systemImage: "checkmark.seal.fill")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.green)
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.green.opacity(0.08))
                    .cornerRadius(10)
            } else {
                Label("Awaiting the other party's signature.", systemImage: "hourglass")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    // MARK: - Progress header (chip row)

    private var progressHeader: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Step \(currentStep.rawValue + 1) of \(BoSStep.allCases.count) — \(currentStep.title)")
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
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(BoSStep.allCases, id: \.self) { step in
                        stepChip(step)
                    }
                }
                .padding(.vertical, 2)
            }
        }
        .padding(16)
        .background(Color(.systemBackground))
    }

    private func stepChip(_ step: BoSStep) -> some View {
        let state = chipState(step)
        let isCurrent = step == currentStep
        return Button {
            currentStep = step
        } label: {
            HStack(spacing: 6) {
                Image(systemName: state.icon)
                    .font(.caption2.weight(.semibold))
                Text(step.title)
                    .font(.caption.weight(isCurrent ? .semibold : .medium))
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .foregroundColor(state.color)
            .background(state.color.opacity(0.12))
            .overlay(
                Capsule().stroke(
                    isCurrent ? state.color : Color.clear,
                    lineWidth: 1.5
                )
            )
            .clipShape(Capsule())
        }
        .buttonStyle(.plain)
    }

    private struct ChipState {
        let icon: String
        let color: Color
    }

    private func chipState(_ step: BoSStep) -> ChipState {
        switch step {
        case .vehicle:
            let missing = vehicleMake.isEmpty || vehicleModel.isEmpty || vin.isEmpty
            return missing
                ? ChipState(icon: "exclamationmark.circle", color: .orange)
                : ChipState(icon: "checkmark.circle.fill", color: .green)
        case .sale:
            let ok = !termsConditions.isEmpty && !saleAmountString.isEmpty
            return ok
                ? ChipState(icon: "checkmark.circle.fill", color: .green)
                : ChipState(icon: "exclamationmark.circle", color: .orange)
        case .seller:
            let missing = sellerName.isEmpty || sellerAddress.isEmpty
            return missing
                ? ChipState(icon: "exclamationmark.circle", color: .orange)
                : ChipState(icon: "checkmark.circle.fill", color: .green)
        case .buyer:
            let missing = buyerName.isEmpty || buyerAddress.isEmpty
            return missing
                ? ChipState(icon: "exclamationmark.circle", color: .orange)
                : ChipState(icon: "checkmark.circle.fill", color: .green)
        case .signature:
            if bos?.isFullySigned == true {
                return ChipState(icon: "checkmark.seal.fill", color: .green)
            }
            if currentRoleHasSigned {
                return ChipState(icon: "hourglass", color: .orange)
            }
            return ChipState(icon: "signature", color: .orange)
        case .review:
            return ChipState(icon: "doc.text", color: .gray)
        }
    }

    // MARK: - Stale banner

    private var staleBanner: some View {
        HStack(spacing: 8) {
            Image(systemName: "arrow.triangle.2.circlepath")
                .font(.caption)
                .foregroundColor(.orange)
            Text("The other party updated the Bill of Sale.")
                .font(.caption.weight(.medium))
                .foregroundColor(.primary)
            Spacer()
            Button("Reload") {
                Task {
                    dirty.removeAll()
                    await loadBoS()
                    showStaleBanner = false
                }
            }
            .font(.caption.weight(.semibold))
            .foregroundColor(.orange)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(Color.orange.opacity(0.10))
    }

    // MARK: - CTA bar

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

            Button {
                Task { await primaryTap() }
            } label: {
                HStack(spacing: 8) {
                    if isSaving || isSigning { ProgressView().tint(.white).scaleEffect(0.85) }
                    Text(primaryLabel)
                        .font(.subheadline.weight(.semibold))
                }
                .frame(maxWidth: .infinity, minHeight: 52)
                .background(primaryDisabled ? Color.driveBaiPrimary.opacity(0.55) : Color.driveBaiPrimary)
                .foregroundColor(.white)
                .cornerRadius(14)
            }
            .disabled(primaryDisabled)
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

    // MARK: - Primary CTA state machine

    private enum PrimaryAction {
        case saveAndContinue
        case continueOnly
        case sign
        case signBlocked   // enabled=false, tells user to save first / complete role fields
        case doneWaiting   // own-signed, other pending
        case done          // both signed, seller viewer OR review
        case dismiss       // close the sheet
    }

    private var primary: PrimaryAction {
        switch currentStep {
        case .vehicle, .sale:
            if !isSeller { return .continueOnly }
            if sellerLockedForEdits { return .continueOnly }
            return dirty.contains(currentStep) ? .saveAndContinue : .continueOnly
        case .seller:
            if !isSeller { return .continueOnly }
            if sellerLockedForEdits { return .continueOnly }
            return dirty.contains(.seller) ? .saveAndContinue : .continueOnly
        case .buyer:
            if !isBuyer { return .continueOnly }
            if buyerLockedForEdits { return .continueOnly }
            return dirty.contains(.buyer) ? .saveAndContinue : .continueOnly
        case .signature:
            if currentRoleHasSigned {
                if otherRoleHasSigned { return .done }
                return .doneWaiting
            }
            if hasPendingEdits { return .signBlocked }
            if !canSign { return .signBlocked }
            return .sign
        case .review:
            return .dismiss
        }
    }

    /// True when at least one editable step (other than signature/review)
    /// has unsaved changes. Used to gate signing.
    private var hasPendingEdits: Bool {
        !dirty.subtracting([.signature, .review]).isEmpty
    }

    /// Required role fields are all populated so signing makes sense.
    private var canSign: Bool {
        if isSeller {
            return !vehicleMake.isEmpty && !vehicleModel.isEmpty && !vin.isEmpty
                && !sellerName.isEmpty && !sellerAddress.isEmpty
        } else if isBuyer {
            return !buyerName.isEmpty && !buyerAddress.isEmpty
        }
        return false
    }

    private var sellerLockedForEdits: Bool { bos?.sellerHasSigned == true }
    private var buyerLockedForEdits: Bool { bos?.buyerHasSigned == true }

    private var primaryLabel: String {
        switch primary {
        case .saveAndContinue: return "Save & continue"
        case .continueOnly:    return currentStep == .buyer ? "Continue" : "Continue"
        case .sign:            return "Sign Bill of Sale"
        case .signBlocked:
            if hasPendingEdits { return "Save changes first" }
            return canSign ? "Sign Bill of Sale" : "Complete your section first"
        case .doneWaiting:
            return isSeller ? "Waiting for buyer signature" : "Waiting for seller signature"
        case .done:            return "Done"
        case .dismiss:         return "Done"
        }
    }

    private var primaryDisabled: Bool {
        if isSaving || isSigning { return true }
        switch primary {
        case .signBlocked, .doneWaiting: return true
        default: return false
        }
    }

    // MARK: - Actions

    private func primaryTap() async {
        switch primary {
        case .saveAndContinue:
            await saveEdits()
        case .continueOnly:
            advance()
        case .sign:
            // The signature pad renders its own capture button; the
            // primary CTA in the .sign state just advances focus down to
            // the pad and hints "please draw + tap Save signature".
            // (Signature capture path stays in the pad's onSave closure.)
            break
        case .signBlocked:
            break
        case .doneWaiting, .done:
            advance()
        case .dismiss:
            dismiss()
        }
    }

    private func advance() {
        if let next = BoSStep(rawValue: currentStep.rawValue + 1) {
            currentStep = next
        }
    }

    private func markDirty(_ step: BoSStep) {
        // Suppress dirty-tracking only WHILE a rehydrate is programmatically
        // writing @State, not for the whole pre-seed window. The prior
        // `hasSeeded` guard silently dropped every keystroke made before
        // the initial GET landed — the async response then rehydrated the
        // now-`clean` fields with the server payload and the user's mid-
        // type edits vanished. `suppressDirtyTracking` is flipped only for
        // the duration of `rehydrateFields(from:)` so genuine typing
        // during a cold-start still registers.
        guard !suppressDirtyTracking else { return }
        dirty.insert(step)
    }

    /// GETs the BoS row and rehydrates local state. On subsequent calls,
    /// only untouched (non-dirty) fields are overwritten so a WS-driven
    /// refresh does not clobber the user's in-flight typing. Fields owned
    /// by the OTHER role are always refreshed from the backend so each
    /// party sees the counterparty's latest saved values.
    private func loadBoS() async {
        do {
            let response = try await APIClient.shared.getBillOfSale(
                purchaseRequestId: purchaseRequest.id
            )
            let domain = response.toDomain()
            bos = domain
            lastServer = domain
            onBoSUpdated(domain)
            rehydrateFields(from: domain)
            hasSeeded = true
        } catch {
            #if DEBUG
            print("[BillOfSaleFlow] getBillOfSale error: \(error)")
            #endif
        }
    }

    /// Overwrites the editable `@State` fields from a backend snapshot.
    /// Rules:
    ///   - Fields owned by the OTHER role are always overwritten (the
    ///     current user can never edit them, so the server truth wins).
    ///   - Fields owned by the CURRENT role are overwritten only if the
    ///     matching step is not dirty AND either the local value is
    ///     empty or this is the first seed. Otherwise the user's in-flight
    ///     typing is preserved.
    ///
    /// While this function runs, `suppressDirtyTracking` is held true so
    /// the `.onChange` handlers on the TextFields don't mistake the
    /// programmatic rewrite for genuine user typing and pollute `dirty`.
    private func rehydrateFields(from domain: BillOfSale) {
        suppressDirtyTracking = true
        defer { suppressDirtyTracking = false }
        // Vehicle + sale + seller-owned by seller role.
        if isSeller {
            let vehicleClean = !dirty.contains(.vehicle)
            let saleClean = !dirty.contains(.sale)
            let sellerClean = !dirty.contains(.seller)
            if vehicleClean {
                vehicleYear = String(domain.vehicleYear)
                vehicleMake = domain.vehicleMake
                vehicleModel = domain.vehicleModel
                vin = domain.vin
            }
            if saleClean {
                termsConditions = domain.termsConditions
                saleAmountString = format(cents: domain.saleAmountCents)
            }
            if sellerClean {
                sellerName = domain.sellerName
                sellerAddress = domain.sellerAddress
            }
        } else {
            // Buyer viewer: vehicle / sale / seller are read-only, so
            // always snap to backend truth.
            vehicleYear = String(domain.vehicleYear)
            vehicleMake = domain.vehicleMake
            vehicleModel = domain.vehicleModel
            vin = domain.vin
            termsConditions = domain.termsConditions
            saleAmountString = format(cents: domain.saleAmountCents)
            sellerName = domain.sellerName
            sellerAddress = domain.sellerAddress
        }

        // Buyer identity fields — mirror of the block above.
        if isBuyer {
            let buyerClean = !dirty.contains(.buyer)
            if buyerClean {
                buyerName = domain.buyerName
                buyerAddress = domain.buyerAddress
            }
        } else {
            buyerName = domain.buyerName
            buyerAddress = domain.buyerAddress
        }
    }

    /// PATCH edits for the current step. Only fields that actually differ
    /// from `lastServer` are sent so an empty string never clobbers a good
    /// server value.
    private func saveEdits() async {
        guard let step = currentStep.saveable, dirty.contains(step) else {
            advance()
            return
        }
        isSaving = true
        defer { isSaving = false }

        do {
            let response: BillOfSaleAPIResponse?
            if isSeller {
                let body = buildSellerPatch(step: step)
                if body.isEmpty {
                    dirty.remove(step)
                    advance()
                    return
                }
                response = try await APIClient.shared.updateBillOfSale(
                    purchaseRequestId: purchaseRequest.id,
                    request: body
                )
            } else if isBuyer, step == .buyer {
                let body = buildBuyerPatch()
                if body.isEmpty {
                    dirty.remove(step)
                    advance()
                    return
                }
                response = try await APIClient.shared.updateBillOfSaleBuyerFields(
                    purchaseRequestId: purchaseRequest.id,
                    request: body
                )
            } else {
                dirty.remove(step)
                advance()
                return
            }

            if let response {
                let domain = response.toDomain()
                bos = domain
                lastServer = domain
                onBoSUpdated(domain)
            }
            dirty.remove(step)
            advance()
        } catch let apiError as APIError {
            // Surface backend copy verbatim (role-specific per new taxonomy).
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func buildSellerPatch(step: BoSStep) -> UpdateBillOfSaleAPIRequest {
        var year: Int? = nil
        var make: String? = nil
        var model: String? = nil
        var vinField: String? = nil
        var terms: String? = nil
        var name: String? = nil
        var address: String? = nil

        if step == .vehicle {
            if let y = Int(vehicleYear), y != lastServer?.vehicleYear { year = y }
            if vehicleMake != lastServer?.vehicleMake { make = vehicleMake }
            if vehicleModel != lastServer?.vehicleModel { model = vehicleModel }
            if vin != lastServer?.vin { vinField = vin }
        }
        if step == .sale {
            if termsConditions != lastServer?.termsConditions { terms = termsConditions }
        }
        if step == .seller {
            if sellerName != lastServer?.sellerName { name = sellerName }
            if sellerAddress != lastServer?.sellerAddress { address = sellerAddress }
        }

        return UpdateBillOfSaleAPIRequest(
            vehicleYear: year,
            vehicleMake: make,
            vehicleModel: model,
            vin: vinField,
            termsConditions: terms,
            sellerName: name,
            sellerAddress: address
        )
    }

    private func buildBuyerPatch() -> UpdateBillOfSaleBuyerFieldsAPIRequest {
        var name: String? = nil
        var address: String? = nil
        if buyerName != lastServer?.buyerName { name = buyerName }
        if buyerAddress != lastServer?.buyerAddress { address = buyerAddress }
        return UpdateBillOfSaleBuyerFieldsAPIRequest(
            buyerName: name,
            buyerAddress: address
        )
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
            lastServer = domain
            onBoSUpdated(domain)

            if let updated = try? await APIClient.shared.fetchPurchaseRequest(
                id: purchaseRequest.id
            ) {
                onPurchaseUpdated(updated.toDomain())
            }

            // Auto-advance to Review after a successful sign so the user
            // sees the receipt immediately rather than a stale signature
            // pad prompt.
            advance()
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    // MARK: - Merge external (WS) update

    /// A WS-driven refresh from ChatViewModel. If nothing is dirty we snap
    /// to the fresh row silently. If the user has unsaved edits we keep
    /// them but flip a banner so they can opt into a reload.
    private func mergeExternalUpdate(_ fresh: BillOfSale) {
        // Only care about newer snapshots.
        if let last = lastServer, fresh.updatedAt <= last.updatedAt {
            return
        }
        bos = fresh
        onBoSUpdated(fresh)
        if dirty.isEmpty {
            lastServer = fresh
            rehydrateFields(from: fresh)
        } else {
            showStaleBanner = true
        }
    }

    // MARK: - Helpers

    private var saleAmountCents: Int64 {
        let clean = saleAmountString.replacingOccurrences(of: ",", with: "")
        return Int64((Double(clean) ?? 0) * 100)
    }

    private var displaySaleAmount: String {
        guard saleAmountCents > 0 else { return "—" }
        let dollars = Double(saleAmountCents) / 100.0
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = "USD"
        formatter.maximumFractionDigits = 0
        return formatter.string(from: NSNumber(value: dollars)) ?? "$\(Int(dollars))"
    }

    private func format(cents: Int64) -> String {
        String(format: "%.2f", Double(cents) / 100.0)
    }

    private func seedInitialState() {
        // Prefer the caller-hinted start step if provided.
        if let initialStep, hasSeeded == false {
            currentStep = initialStep
        }

        // Seed from cache. `loadBoS` will overwrite once the network
        // round-trip completes.
        let existing = initialBoS
        bos = existing
        lastServer = existing

        vehicleYear = existing.map { String($0.vehicleYear) } ?? extractYear(purchaseRequest.carTitle)
        vehicleMake = existing?.vehicleMake ?? ""
        vehicleModel = existing?.vehicleModel ?? ""
        vin = existing?.vin ?? ""

        let cents = existing?.saleAmountCents ?? purchaseRequest.offerAmountCents
        saleAmountString = format(cents: cents)

        termsConditions = existing?.termsConditions
            ?? "Vehicle is sold as-is, where-is, with no warranties unless otherwise stated in writing."
        sellerName = existing?.sellerName ?? purchaseRequest.sellerName
        sellerAddress = existing?.sellerAddress ?? ""
        buyerName = existing?.buyerName ?? purchaseRequest.buyerName
        buyerAddress = existing?.buyerAddress ?? ""

        if existing != nil { hasSeeded = true }
    }

    private func extractYear(_ carTitle: String) -> String {
        // Best-effort: grab a leading 4-digit chunk from strings like
        // "2019 Honda Civic". Falls back to empty rather than a bogus 4
        // characters.
        let leading4 = String(carTitle.prefix(4))
        return Int(leading4) != nil ? leading4 : ""
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

/// Public so ChatView can hint the wizard at an initial step.
enum BoSStep: Int, CaseIterable {
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

    /// Which steps produce a PATCH when the user taps Save & continue.
    var saveable: BoSStep? {
        switch self {
        case .vehicle, .sale, .seller, .buyer: return self
        case .signature, .review: return nil
        }
    }
}

// MARK: - Empty-body helpers for API request structs

fileprivate extension UpdateBillOfSaleAPIRequest {
    var isEmpty: Bool {
        vehicleYear == nil && vehicleMake == nil && vehicleModel == nil
            && vin == nil && termsConditions == nil
            && sellerName == nil && sellerAddress == nil
    }
}

fileprivate extension UpdateBillOfSaleBuyerFieldsAPIRequest {
    var isEmpty: Bool {
        buyerName == nil && buyerAddress == nil
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
