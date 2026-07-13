import SwiftUI
import CoreLocation

/// Multi-step wizard for the Bill of Sale.  Covers standard Vehicle Bill of
/// Sale fields (seller/buyer, vehicle, price, signatures) —
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
    @State private var sellerAddressLat: Double? = nil
    @State private var sellerAddressLng: Double? = nil
    @State private var buyerName: String = ""
    @State private var buyerAddress: String = ""
    @State private var buyerAddressLat: Double? = nil
    @State private var buyerAddressLng: Double? = nil
    @State private var selectedTitleCondition: TitleCondition? = nil
    @State private var titleConditionOther: String = ""

    /// Drives the map-picker / document-preview sheets (a single enum-backed
    /// `.sheet(item:)` so multiple presentations never collide).
    @State private var activeSheet: ActiveSheet? = nil
    /// Drives the shared document source chooser for the seller's title upload.
    @State private var showTitleSourcePicker = false
    @State private var isUploadingTitle = false
    /// Surfaced (with a Retry) when the cold-start GET fails and we have no
    /// cached row to fall back on. Never silently swallowed.
    @State private var loadError: String? = nil

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

    /// A backend-signed document reference to preview (ID or title).
    private struct PreviewDoc: Identifiable, Equatable {
        let id = UUID()
        let url: String
        let filename: String
    }

    /// Single presentation channel for the map pickers and document previews.
    private enum ActiveSheet: Identifiable, Equatable {
        case sellerAddress
        case buyerAddress
        case preview(PreviewDoc)

        var id: String {
            switch self {
            case .sellerAddress: return "sellerAddress"
            case .buyerAddress: return "buyerAddress"
            case .preview(let doc): return "preview-\(doc.id.uuidString)"
            }
        }
    }

    private var sellerCoordinate: CLLocationCoordinate2D? {
        guard let lat = sellerAddressLat, let lng = sellerAddressLng,
              lat != 0 || lng != 0 else { return nil }
        return CLLocationCoordinate2D(latitude: lat, longitude: lng)
    }

    private var buyerCoordinate: CLLocationCoordinate2D? {
        guard let lat = buyerAddressLat, let lng = buyerAddressLng,
              lat != 0 || lng != 0 else { return nil }
        return CLLocationCoordinate2D(latitude: lat, longitude: lng)
    }

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
                    // Cold start with no cached row shows the full placeholder +
                    // Retry inside the step (gated on !hasSeeded). A REFRESH
                    // failure while cached content is on screen (hasSeeded) must
                    // still surface — this compact banner covers that case so a
                    // stale BoS can't silently hide its own load error.
                    if hasSeeded, let loadError { loadErrorBanner(loadError) }
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
            .sheet(item: $activeSheet) { sheet in
                switch sheet {
                case .sellerAddress:
                    CarPickupLocationPickerView(initialCoordinate: sellerCoordinate) { coord, resolved in
                        applySellerAddress(coord: coord, resolved: resolved)
                    }
                case .buyerAddress:
                    CarPickupLocationPickerView(initialCoordinate: buyerCoordinate) { coord, resolved in
                        applyBuyerAddress(coord: coord, resolved: resolved)
                    }
                case .preview(let doc):
                    DocumentPreviewSheet(source: .remoteURL(doc.url, filename: doc.filename))
                }
            }
            .documentSourcePicker(isPresented: $showTitleSourcePicker, filenameBase: "title") { picked in
                Task { await uploadTitle(picked) }
            }
        }
        // Product-tour host lives INSIDE the BoS sheet (a root-level overlay
        // does not cover a presented sheet); re-inject the shared coordinator
        // because a sheet gets a fresh environment.
        .environmentObject(ProductTourCoordinator.shared)
        .onboardingOverlayHost(ProductTourCoordinator.shared)
        .onAppear { ProductTourCoordinator.shared.handle(.bosOpened) }
        .onChange(of: bos) { _, newValue in
            guard let newValue, newValue.isFullySigned else { return }
            if newValue.finalizedPdfUrl?.isEmpty == false {
                ProductTourCoordinator.shared.updateContext { $0.pdfReady = true }
            }
            ProductTourCoordinator.shared.handle(.bothSignaturesPresent)
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
        .onboardingTarget(.bosFirstSection)
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
            titleCard(allowEditing: isSeller && !sellerLockedForEdits)
        }
    }

    // MARK: - Title section (condition + document)

    /// Title condition + document affordances. Rendered in both the sale step
    /// (seller edits) and the review step (read-only). `allowEditing` gates the
    /// picker; the upload/view/warning affordances are role-driven regardless.
    private func titleCard(allowEditing: Bool) -> some View {
        PurchaseSectionCard(title: "Title") {
            VStack(alignment: .leading, spacing: 12) {
                if allowEditing {
                    titleConditionPicker
                    if selectedTitleCondition == .other {
                        PurchaseFormField("Describe title condition", text: $titleConditionOther)
                            .onChange(of: titleConditionOther) { _, _ in markDirty(.sale) }
                    }
                } else {
                    reviewRow("Condition", selectedTitleCondition == nil
                              ? "" : titleConditionReadOnlyText,
                              requiredMissingHint: "Missing — seller must complete")
                }

                titleDocumentAffordance

                if isBuyer {
                    Label("Review the vehicle title carefully before accepting the vehicle.",
                          systemImage: "exclamationmark.triangle.fill")
                        .font(.caption.weight(.medium))
                        .foregroundColor(.orange)
                        .padding(10)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color.orange.opacity(0.10))
                        .cornerRadius(10)
                }
            }
        }
    }

    private var titleConditionReadOnlyText: String {
        guard let cond = selectedTitleCondition else { return "" }
        if cond == .other {
            let detail = titleConditionOther.trimmingCharacters(in: .whitespacesAndNewlines)
            return detail.isEmpty ? cond.displayText : "\(cond.displayText) — \(detail)"
        }
        return cond.displayText
    }

    private var titleConditionPicker: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Condition")
                .font(.caption.weight(.medium))
                .foregroundColor(.secondary)
            Menu {
                ForEach(TitleCondition.allCases) { cond in
                    Button {
                        selectedTitleCondition = cond
                        markDirty(.sale)
                    } label: {
                        if selectedTitleCondition == cond {
                            Label(cond.displayText, systemImage: "checkmark")
                        } else {
                            Text(cond.displayText)
                        }
                    }
                }
            } label: {
                HStack {
                    Text(selectedTitleCondition?.displayText ?? "Select title condition")
                        .font(.subheadline)
                        .foregroundColor(selectedTitleCondition == nil ? .secondary : .primary)
                    Spacer()
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                .padding(.horizontal, 11)
                .padding(.vertical, 11)
                .background(Color(.systemGray6))
                .cornerRadius(9)
            }
        }
    }

    /// View / upload the vehicle title. When the title is on file both roles
    /// can view it; when it is missing only the seller sees the upload prompt.
    @ViewBuilder
    private var titleDocumentAffordance: some View {
        if bos?.titleUploaded == true, let url = bos?.titleDocumentUrl, !url.isEmpty {
            Button {
                activeSheet = .preview(PreviewDoc(url: url, filename: "Vehicle Title"))
            } label: {
                Label("View Title", systemImage: "doc.text.magnifyingglass")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.driveBaiPrimary)
            }
            .buttonStyle(.plain)
        } else if isSeller {
            Button {
                showTitleSourcePicker = true
            } label: {
                HStack(spacing: 8) {
                    if isUploadingTitle {
                        ProgressView().scaleEffect(0.8)
                    } else {
                        Image(systemName: "arrow.up.doc.fill")
                    }
                    Text(isUploadingTitle
                         ? "Uploading title…"
                         : "Upload the vehicle title (required to complete the sale)")
                        .font(.caption.weight(.semibold))
                        .multilineTextAlignment(.leading)
                }
                .foregroundColor(.driveBaiPrimary)
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color.driveBaiPrimary.opacity(0.08))
                .cornerRadius(10)
            }
            .buttonStyle(.plain)
            .disabled(isUploadingTitle)
        } else {
            Label("The seller hasn't uploaded the vehicle title yet.",
                  systemImage: "clock.badge.exclamationmark")
                .font(.caption)
                .foregroundColor(.secondary)
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
                    .disabled(!isSeller || sellerLockedForEdits)
                addressPickerRow(
                    address: sellerAddress,
                    editable: isSeller && !sellerLockedForEdits,
                    onEdit: { activeSheet = .sellerAddress }
                )
            }
            if bos?.sellerIdDocumentUrl != nil {
                viewIdRow(url: bos?.sellerIdDocumentUrl, filename: "Seller ID")
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
    }

    private var buyerStep: some View {
        PurchaseSectionCard(title: "Buyer identity") {
            VStack(spacing: 10) {
                PurchaseFormField("Legal name", text: $buyerName)
                    .onChange(of: buyerName) { _, _ in markDirty(.buyer) }
                    .disabled(!isBuyer || buyerLockedForEdits)
                addressPickerRow(
                    address: buyerAddress,
                    editable: isBuyer && !buyerLockedForEdits,
                    onEdit: { activeSheet = .buyerAddress }
                )
            }
            if bos?.buyerIdDocumentUrl != nil {
                viewIdRow(url: bos?.buyerIdDocumentUrl, filename: "Buyer ID")
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
    }

    // MARK: - Address picker + document rows

    /// Map-backed address field. Tapping opens `CarPickupLocationPickerView`,
    /// which resolves a real street address from the map center (defaulting to
    /// the device fix when empty) — so we never persist a bare coordinate.
    private func addressPickerRow(
        address: String,
        editable: Bool,
        onEdit: @escaping () -> Void
    ) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Address")
                .font(.caption.weight(.medium))
                .foregroundColor(.secondary)
            Button(action: onEdit) {
                HStack(spacing: 8) {
                    Image(systemName: "mappin.and.ellipse")
                        .foregroundColor(.driveBaiPrimary)
                    Text(address.isEmpty ? "Select address on map" : address)
                        .font(.subheadline)
                        .foregroundColor(address.isEmpty ? .secondary : .primary)
                        .multilineTextAlignment(.leading)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if editable {
                        Image(systemName: "chevron.right")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }
                .padding(.horizontal, 11)
                .padding(.vertical, 11)
                .background(Color(.systemGray6))
                .cornerRadius(9)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(!editable)
            if editable {
                Text("Required to sign. Pick the location on the map.")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }
        }
    }

    /// "View ID" row — shown only when a signed ID document is on file. Never
    /// blocks on a missing document.
    @ViewBuilder
    private func viewIdRow(url: String?, filename: String) -> some View {
        if let url, !url.isEmpty {
            Button {
                activeSheet = .preview(PreviewDoc(url: url, filename: filename))
            } label: {
                Label("View ID", systemImage: "person.text.rectangle")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.driveBaiPrimary)
            }
            .buttonStyle(.plain)
        }
    }

    @ViewBuilder
    private var signatureStep: some View {
        if !hasSeeded {
            notLoadedPlaceholder
        } else {
            signatureStepContent
        }
    }

    private var signatureStepContent: some View {
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
            } else if hasPendingEditsForMe {
                // Guard: force the user to save THEIR OWN pending edits
                // before signing. Role-scoped so a buyer isn't blocked
                // because seller-owned fields are dirty (they can't be, in
                // practice — the buyer never mounts those steps — but the
                // gate stays formally correct across future refactors).
                Text("Save your pending changes before signing.")
                    .font(.subheadline)
                    .foregroundColor(.orange)
                    .padding(12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color.orange.opacity(0.08))
                    .cornerRadius(10)
            } else if !canSign {
                // Section incomplete (missing name/address/vehicle). The pad is
                // withheld — not just the bottom CTA — so the UI can't
                // contradict itself with an enabled Save-signature button above
                // a disabled "Complete your section first" CTA. The backend
                // SELLER_ADDRESS_REQUIRED/BUYER_ADDRESS_REQUIRED gate is the
                // backstop; this stops the user from ever reaching the round-trip.
                Text(isSeller
                     ? "Add your name, address, and the vehicle details before signing."
                     : "Add your name and address before signing.")
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
        .onboardingTarget(.bosSignedSection)
    }

    @ViewBuilder
    private var reviewStep: some View {
        if !hasSeeded {
            notLoadedPlaceholder
        } else {
            reviewStepContent
        }
    }

    private var reviewStepContent: some View {
        VStack(alignment: .leading, spacing: 16) {
            PurchaseSectionCard(title: "Vehicle") {
                reviewRow("Year", vehicleYear)
                reviewRow("Make", vehicleMake, requiredMissingHint: missingSellerHint)
                reviewRow("Model", vehicleModel, requiredMissingHint: missingSellerHint)
                reviewRow("VIN", vin, requiredMissingHint: missingSellerHint)
            }
            PurchaseSectionCard(title: "Sale") {
                reviewRow("Amount", displaySaleAmount)
                reviewRow("Terms", termsConditions.isEmpty ? "—" : termsConditions)
            }
            titleCard(allowEditing: false)
            PurchaseSectionCard(title: "Seller") {
                reviewRow("Name", sellerName, requiredMissingHint: missingSellerHint)
                reviewRow("Address", sellerAddress, requiredMissingHint: missingSellerHint)
                if bos?.sellerIdDocumentUrl != nil {
                    viewIdRow(url: bos?.sellerIdDocumentUrl, filename: "Seller ID")
                }
                reviewSignatureRow(signed: bos?.sellerHasSigned == true, label: "Seller")
            }
            PurchaseSectionCard(title: "Buyer") {
                reviewRow("Name", buyerName, requiredMissingHint: missingBuyerHint)
                reviewRow("Address", buyerAddress, requiredMissingHint: missingBuyerHint)
                if bos?.buyerIdDocumentUrl != nil {
                    viewIdRow(url: bos?.buyerIdDocumentUrl, filename: "Buyer ID")
                }
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

            // Signed PDF (or a "Preparing…" placeholder) once both parties
            // have signed. Preview + Share / Save to Files.
            //
            // Tagged as the `pdf_ready` target: the signature/PDF teach fires
            // while this sheet is frontmost, so the spotlight has to resolve
            // against the row in *this* subtree, not the chat card's copy.
            BillOfSalePDFRow(billOfSale: bos, isTourTarget: true)
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
            // Title condition is required before the buyer can accept, so nudge
            // the seller here too (buyers can't edit it — don't gate their chip).
            let titleOk = !isSeller || selectedTitleCondition != nil
            let ok = !termsConditions.isEmpty && !saleAmountString.isEmpty && titleOk
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
            // Never allow signing before the BoS row has loaded — the fields
            // would be blank/unseeded.
            if !hasSeeded { return .signBlocked }
            if hasPendingEditsForMe { return .signBlocked }
            if !canSign { return .signBlocked }
            return .sign
        case .review:
            return .dismiss
        }
    }

    /// True when at least one editable step (other than signature/review)
    /// has unsaved changes across BOTH roles. Kept for close-guard / union
    /// callers only; never gate the current actor's Sign CTA with this —
    /// use `hasPendingEditsForMe` so the buyer isn't blocked by the seller
    /// having dirty seller-only fields (and vice versa).
    private var hasPendingEditsAny: Bool {
        !dirty.subtracting([.signature, .review]).isEmpty
    }

    /// Steps whose editable fields are owned by the seller (and mounted
    /// only when `isSeller == true`).
    private var sellerOwnedSteps: Set<BoSStep> { [.vehicle, .sale, .seller] }
    /// Steps whose editable fields are owned by the buyer.
    private var buyerOwnedSteps: Set<BoSStep> { [.buyer] }

    /// Steps the CURRENT viewer owns. `.signature` and `.review` are never
    /// role-owned — they aren't PATCH targets.
    private var myOwnedSteps: Set<BoSStep> {
        if isSeller { return sellerOwnedSteps }
        if isBuyer  { return buyerOwnedSteps }
        return []
    }

    /// Role-scoped gate for the Sign CTA + "save changes first" banner.
    /// Only the current viewer's own dirty steps block them from signing.
    private var hasPendingEditsForMe: Bool {
        !dirty.intersection(myOwnedSteps).isEmpty
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
            if !hasSeeded { return "Loading Bill of Sale…" }
            if hasPendingEditsForMe { return "Save changes first" }
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
            applyServerBOS(domain, source: .load)
            hasSeeded = true
            loadError = nil
        } catch let apiError as APIError {
            // Do NOT swallow: the wizard opens in a BoS stage, so the row
            // should exist. Surface a retryable error instead of leaving the
            // user on a blank Review/Signature.
            loadError = apiError.errorDescription ?? "Couldn't load the Bill of Sale."
            #if DEBUG
            print("[BillOfSaleFlow] getBillOfSale error: \(apiError)")
            #endif
        } catch {
            loadError = error.localizedDescription
            #if DEBUG
            print("[BillOfSaleFlow] getBillOfSale error: \(error)")
            #endif
        }
    }

    // MARK: - Address + title actions

    private func applySellerAddress(coord: CLLocationCoordinate2D, resolved: ResolvedAddress) {
        // Persist the resolved human address string + coordinate — never a bare
        // map center. `displaySummary` always yields a non-empty label.
        sellerAddress = resolved.displaySummary
        sellerAddressLat = coord.latitude
        sellerAddressLng = coord.longitude
        markDirty(.seller)
    }

    private func applyBuyerAddress(coord: CLLocationCoordinate2D, resolved: ResolvedAddress) {
        buyerAddress = resolved.displaySummary
        buyerAddressLat = coord.latitude
        buyerAddressLng = coord.longitude
        markDirty(.buyer)
    }

    /// Uploads a car `title` document, then refreshes the BoS so the derived
    /// `title_uploaded` / `title_document_url` reflect it. Uses an edit-
    /// preserving refresh (not `loadBoS`) so an unsaved title-condition edit in
    /// the same card isn't silently dropped by a dirty-set reset.
    private func uploadTitle(_ picked: PickedDocument) async {
        guard !isUploadingTitle else { return }
        isUploadingTitle = true
        defer { isUploadingTitle = false }
        do {
            _ = try await APIClient.shared.uploadCarDocument(
                carId: purchaseRequest.carId,
                documentType: .title,
                fileData: picked.data,
                filename: picked.filename,
                mimeType: picked.mimeType
            )
            if let response = try? await APIClient.shared.getBillOfSale(
                purchaseRequestId: purchaseRequest.id
            ) {
                let domain = response.toDomain()
                // Update the read-model (title_uploaded / signed URLs) and the
                // diff baseline, but keep the user's in-flight edits + their
                // dirty flags: rehydrateFields only overwrites clean fields.
                bos = domain
                lastServer = domain
                rehydrateFields(from: domain)
                onBoSUpdated(domain)
                hasSeeded = true
                loadError = nil
            }
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    /// Discriminates the caller so `applyServerBOS` knows which subset of
    /// the local dirty state a fresh server snapshot invalidates.
    private enum BOSApplySource {
        /// A PATCH just succeeded for `step` — only that step's dirt is
        /// cleared. Other in-flight edits (in principle: none, since only
        /// one step is mounted at a time, but defensive) stay dirty.
        case save(BoSStep)
        /// A GET (cold-start or explicit reload) — the server payload is
        /// authoritative, all local dirt is cleared.
        case load
        /// A WS-driven counterparty update when we had no unsaved edits —
        /// same semantics as load.
        case externalUpdate
    }

    /// Adopt a server-canonical BoS snapshot without triggering the
    /// `.onChange`-driven `markDirty` handlers on the buyer/seller text
    /// fields. This is the single choke-point every "server returned
    /// canonical state" path funnels through — `loadBoS`, `saveEdits`, and
    /// `mergeExternalUpdate`.
    ///
    /// Why the suppression window matters: after a successful PATCH, we
    /// swap the currently-mounted step (typically buyer or seller). SwiftUI
    /// coalesces the state changes into one view update, and the outgoing
    /// TextField bindings re-evaluate their `.onChange` handlers once
    /// more — which, without `suppressDirtyTracking`, re-inserts the just-
    /// saved step back into `dirty`. That "phantom re-dirty" is what left
    /// the buyer stuck on "Save changes first" immediately after saving.
    private func applyServerBOS(_ domain: BillOfSale, source: BOSApplySource) {
        let prior = suppressDirtyTracking
        suppressDirtyTracking = true
        defer { suppressDirtyTracking = prior }

        bos = domain
        lastServer = domain
        rehydrateFields(from: domain)

        switch source {
        case .save(let step):
            dirty.remove(step)
        case .load, .externalUpdate:
            dirty.removeAll()
        }

        onBoSUpdated(domain)
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
        // Save-and-restore rather than force-`false` because this can be
        // called from inside `applyServerBOS`, which is already holding
        // the suppression window open. A raw `false` on exit would prem-
        // aturely re-arm dirty tracking while the outer defer still had
        // more programmatic writes to apply.
        let prior = suppressDirtyTracking
        suppressDirtyTracking = true
        defer { suppressDirtyTracking = prior }
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
                selectedTitleCondition = domain.titleCondition
                titleConditionOther = domain.titleConditionOther ?? ""
            }
            if sellerClean {
                sellerName = domain.sellerName
                sellerAddress = domain.sellerAddress
                sellerAddressLat = domain.sellerAddressLat
                sellerAddressLng = domain.sellerAddressLng
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
            selectedTitleCondition = domain.titleCondition
            titleConditionOther = domain.titleConditionOther ?? ""
            sellerName = domain.sellerName
            sellerAddress = domain.sellerAddress
            sellerAddressLat = domain.sellerAddressLat
            sellerAddressLng = domain.sellerAddressLng
        }

        // Buyer identity fields — mirror of the block above.
        if isBuyer {
            let buyerClean = !dirty.contains(.buyer)
            if buyerClean {
                buyerName = domain.buyerName
                buyerAddress = domain.buyerAddress
                buyerAddressLat = domain.buyerAddressLat
                buyerAddressLng = domain.buyerAddressLng
            }
        } else {
            buyerName = domain.buyerName
            buyerAddress = domain.buyerAddress
            buyerAddressLat = domain.buyerAddressLat
            buyerAddressLng = domain.buyerAddressLng
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
        // "Other" title condition requires a description (server enforces this
        // too). Block the save so the user isn't bounced by a 400.
        if isSeller, step == .sale, selectedTitleCondition == .other,
           titleConditionOther.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            errorMessage = "Please describe the title condition (required when it's set to “Other”)."
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
                // Funnel through applyServerBOS so the buyer/seller
                // TextField `.onChange` handlers can't phantom-re-dirty
                // the step we just saved while SwiftUI is coalescing the
                // step-swap into a single view update. This is what
                // unblocked the buyer's "Save changes first" wedge.
                applyServerBOS(domain, source: .save(step))
            } else {
                // No-op PATCH branch (empty body): just clear the step so
                // the CTA moves out of the "save required" state.
                dirty.remove(step)
            }
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
        var addressLat: Double? = nil
        var addressLng: Double? = nil
        var titleCond: String? = nil
        var titleCondOther: String? = nil

        if step == .vehicle {
            if let y = Int(vehicleYear), y != lastServer?.vehicleYear { year = y }
            if vehicleMake != lastServer?.vehicleMake { make = vehicleMake }
            if vehicleModel != lastServer?.vehicleModel { model = vehicleModel }
            if vin != lastServer?.vin { vinField = vin }
        }
        if step == .sale {
            if termsConditions != lastServer?.termsConditions { terms = termsConditions }
            let condRaw = selectedTitleCondition?.rawValue
            let condChanged = condRaw != lastServer?.titleCondition?.rawValue
            let otherChanged = titleConditionOther != (lastServer?.titleConditionOther ?? "")
            if condChanged { titleCond = condRaw }
            // The server requires title_condition_other whenever the condition
            // is "other"; send it if either the condition or the text moved.
            if selectedTitleCondition == .other, condChanged || otherChanged {
                titleCondOther = titleConditionOther
            }
        }
        if step == .seller {
            if sellerName != lastServer?.sellerName { name = sellerName }
            if sellerAddress != lastServer?.sellerAddress
                || sellerAddressLat != lastServer?.sellerAddressLat
                || sellerAddressLng != lastServer?.sellerAddressLng {
                address = sellerAddress
                addressLat = sellerAddressLat
                addressLng = sellerAddressLng
            }
        }

        return UpdateBillOfSaleAPIRequest(
            vehicleYear: year,
            vehicleMake: make,
            vehicleModel: model,
            vin: vinField,
            termsConditions: terms,
            sellerName: name,
            sellerAddress: address,
            sellerAddressLat: addressLat,
            sellerAddressLng: addressLng,
            titleCondition: titleCond,
            titleConditionOther: titleCondOther
        )
    }

    private func buildBuyerPatch() -> UpdateBillOfSaleBuyerFieldsAPIRequest {
        var name: String? = nil
        var address: String? = nil
        var addressLat: Double? = nil
        var addressLng: Double? = nil
        if buyerName != lastServer?.buyerName { name = buyerName }
        if buyerAddress != lastServer?.buyerAddress
            || buyerAddressLat != lastServer?.buyerAddressLat
            || buyerAddressLng != lastServer?.buyerAddressLng {
            address = buyerAddress
            addressLat = buyerAddressLat
            addressLng = buyerAddressLng
        }
        return UpdateBillOfSaleBuyerFieldsAPIRequest(
            buyerName: name,
            buyerAddress: address,
            buyerAddressLat: addressLat,
            buyerAddressLng: addressLng
        )
    }

    private func submitSignature(data: Data) async {
        guard !isSigning else { return }
        // Defensive: the pad is already withheld until canSign, but never let a
        // signature POST fire for an incomplete section (empty address/name/VIN)
        // — the backend would 400 with SELLER_ADDRESS_REQUIRED anyway.
        guard canSign else {
            errorMessage = isSeller
                ? "Add your name, address, and the vehicle details before signing."
                : "Add your name and address before signing."
            return
        }
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
        if dirty.isEmpty {
            // No unsaved edits — snap silently.
            applyServerBOS(fresh, source: .externalUpdate)
        } else {
            // Keep the user's in-flight edits, but let them know a fresh
            // counterparty snapshot arrived. `bos` still moves so read-only
            // views (e.g. review's "seller signed" chip) reflect the new
            // state; local editable fields stay untouched until the user
            // taps Reload.
            bos = fresh
            onBoSUpdated(fresh)
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
        // One-shot: a re-entrant .onAppear must never clobber state the first
        // GET already loaded (which would drop a cold-start-loaded server row
        // back to the initialBoS/purchase fallbacks while still reading as
        // seeded). loadBoS owns all refreshes after the first seed.
        guard !hasSeeded else { return }

        // Prefer the caller-hinted start step if provided.
        if let initialStep {
            currentStep = initialStep
        }

        // Seed from cache. `loadBoS` will overwrite once the network
        // round-trip completes.
        let existing = initialBoS
        bos = existing
        lastServer = existing

        // Seed vehicle identity from the BoS row when present, else fall back
        // to the vehicle fields now mirrored onto the purchase response (and
        // only then to a best-effort year parse from the car title).
        vehicleYear = existing.map { String($0.vehicleYear) }
            ?? (purchaseRequest.vehicleYear > 0
                ? String(purchaseRequest.vehicleYear)
                : extractYear(purchaseRequest.carTitle))
        vehicleMake = existing?.vehicleMake ?? purchaseRequest.vehicleMake
        vehicleModel = existing?.vehicleModel ?? purchaseRequest.vehicleModel
        vin = existing?.vin ?? purchaseRequest.vehicleVin

        let cents = existing?.saleAmountCents ?? purchaseRequest.offerAmountCents
        saleAmountString = format(cents: cents)

        termsConditions = existing?.termsConditions
            ?? "Vehicle is sold as-is, where-is, with no warranties unless otherwise stated in writing."
        sellerName = existing?.sellerName ?? purchaseRequest.sellerName
        sellerAddress = existing?.sellerAddress ?? ""
        sellerAddressLat = existing?.sellerAddressLat
        sellerAddressLng = existing?.sellerAddressLng
        buyerName = existing?.buyerName ?? purchaseRequest.buyerName
        buyerAddress = existing?.buyerAddress ?? ""
        buyerAddressLat = existing?.buyerAddressLat
        buyerAddressLng = existing?.buyerAddressLng
        selectedTitleCondition = existing?.titleCondition
        titleConditionOther = existing?.titleConditionOther ?? ""

        if existing != nil { hasSeeded = true }
    }

    private func extractYear(_ carTitle: String) -> String {
        // Best-effort: grab a leading 4-digit chunk from strings like
        // "2019 Honda Civic". Falls back to empty rather than a bogus 4
        // characters.
        let leading4 = String(carTitle.prefix(4))
        return Int(leading4) != nil ? leading4 : ""
    }

    /// Shown for an empty required field so the Review never renders a bare
    /// "—" for a value the seller still has to complete.
    private var missingSellerHint: String { "Missing — seller must complete" }
    private var missingBuyerHint: String { "Missing — buyer must complete" }

    private func reviewRow(_ label: String, _ value: String, requiredMissingHint: String? = nil) -> some View {
        HStack(alignment: .top) {
            Text(label)
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(width: 90, alignment: .leading)
            if value.isEmpty, let hint = requiredMissingHint {
                Text(hint)
                    .font(.subheadline.weight(.medium))
                    .foregroundColor(.orange)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                Text(value.isEmpty ? "—" : value)
                    .font(.subheadline)
                    .foregroundColor(.primary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    // MARK: - Not-loaded placeholder + load-error banner

    /// Shown in the Review / Signature steps until we have a seeded BoS row, so
    /// those steps NEVER render blank make/model/VIN or allow signing a blank.
    private var notLoadedPlaceholder: some View {
        VStack(spacing: 14) {
            if let loadError {
                Image(systemName: "exclamationmark.triangle")
                    .font(.system(size: 34))
                    .foregroundColor(.orange)
                Text("Couldn't load the Bill of Sale")
                    .font(.headline)
                Text(loadError)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                Button {
                    Task { await loadBoS() }
                } label: {
                    Text("Retry")
                        .font(.subheadline.weight(.semibold))
                        .foregroundColor(.white)
                        .padding(.horizontal, 24)
                        .padding(.vertical, 10)
                        .background(Color.driveBaiPrimary)
                        .cornerRadius(12)
                }
            } else {
                ProgressView()
                Text("Loading the Bill of Sale…")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 44)
    }

    private func loadErrorBanner(_ message: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill")
                .font(.caption)
                .foregroundColor(.orange)
            Text("Couldn't load the latest Bill of Sale.")
                .font(.caption.weight(.medium))
                .foregroundColor(.primary)
            Spacer()
            Button("Retry") {
                Task { await loadBoS() }
            }
            .font(.caption.weight(.semibold))
            .foregroundColor(.orange)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(Color.orange.opacity(0.10))
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
            && sellerAddressLat == nil && sellerAddressLng == nil
            && titleCondition == nil && titleConditionOther == nil
    }
}

fileprivate extension UpdateBillOfSaleBuyerFieldsAPIRequest {
    var isEmpty: Bool {
        buyerName == nil && buyerAddress == nil
            && buyerAddressLat == nil && buyerAddressLng == nil
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
