import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

// Minimum weekly rent price: 1 in debug builds (testing), 50 in release (production).
#if DEBUG
private let kMinWeeklyRentPrice: Double = 1
#else
private let kMinWeeklyRentPrice: Double = 50
#endif

/// Save-state used by the autosave pill in CarDetailEditView's toolbar.
///
/// .idle is the at-rest state when nothing has been touched yet; once the
/// user edits anything we flip to .dirty (debounce running), then .saving
/// (network in flight), then either .saved or .failed(reason). The pill
/// renders one of {checkmark, spinner, retry button} accordingly.
enum SaveState: Equatable {
    case idle
    case dirty
    case saving
    case saved
    case failed(String)
}

/// Car detail edit mode with collapsible sections - Figma accurate
struct CarDetailEditView: View {
    let car: Car
    @Binding var isEditMode: Bool
    @StateObject private var store = OwnerCarsStore.shared
    @Environment(\.dismiss) private var dismiss

    // Edit state
    @State private var editedCar: Car
    @State private var showPhotosEditor: Bool = false
    @State private var showDeleteConfirmation: Bool = false
    @State private var showEditLocation: Bool = false

    // Loading and error state
    @State private var isSaving: Bool = false
    @State private var isDeleting: Bool = false
    @State private var isTogglingPause: Bool = false
    @State private var errorMessage: String?
    @State private var showErrorAlert: Bool = false

    /// Verbatim server message from a SALE_REQUIREMENTS_NOT_MET 400 (QA
    /// pt 8) — rendered inline under the For-sale toggle alongside the
    /// client-side checklist. Cleared on the next successful save.
    @State private var saleRequirementsMessage: String?

    // --- Autosave state ---
    //
    // Edits to any field of `editedCar` are persisted automatically after
    // a short debounce so a user who walks away from the screen doesn't
    // lose their changes. The Save button in the toolbar is repurposed
    // into a save-state pill ("Saved ✓ / Saving… / Retry") to make the
    // current state legible at a glance.
    //
    // Race safety: only ONE save is in-flight at a time (`inFlightSave`).
    // If the user types DURING a save, the change is captured by the
    // post-save Equatable check (snapshotBefore != editedCar) and a
    // follow-up save is scheduled. This converges to last-write-wins
    // without overlapping requests.
    @State private var saveState: SaveState = .idle
    @State private var autosaveTask: Task<Void, Never>?
    @State private var inFlightSave: Bool = false
    /// Set after the view has appeared once so we don't race the
    /// initial `editedCar` assignment in `init` with an autosave.
    @State private var hasAppeared: Bool = false

    // Section expansion state
    @State private var isGeneralExpanded: Bool = true
    @State private var isFeaturesExpanded: Bool = false
    @State private var isRequirementsExpanded: Bool = false
    @State private var isDocumentsExpanded: Bool = false

    init(car: Car, isEditMode: Binding<Bool>) {
        self.car = car
        self._isEditMode = isEditMode
        self._editedCar = State(initialValue: car)
    }

    var body: some View {
        ZStack(alignment: .bottom) {
            ScrollView {
                VStack(spacing: 0) {
                    // Photo carousel with swipeable photos
                    CarPhotoCarousel(
                        photoSlots: editedCar.photoSlots,
                        onEditPhotos: { showPhotosEditor = true }
                    )

                    // Collapsible sections
                    VStack(spacing: 0) {
                        // General Information
                        CollapsibleSection(
                            title: "General information",
                            isExpanded: $isGeneralExpanded
                        ) {
                            GeneralInformationContent(
                                car: $editedCar,
                                saleRequirementsMessage: saleRequirementsMessage
                            )
                        }

                        Divider()

                        // Core Features
                        CollapsibleSection(
                            title: "Core features",
                            isExpanded: $isFeaturesExpanded
                        ) {
                            CoreFeaturesContent(specs: $editedCar.specs)
                        }

                        Divider()

                        // Requirements
                        CollapsibleSection(
                            title: "Requirements",
                            isExpanded: $isRequirementsExpanded
                        ) {
                            RequirementsContent(requirements: $editedCar.requirements)
                        }

                        Divider()

                        // Documents
                        CollapsibleSection(
                            title: "Documents",
                            isExpanded: $isDocumentsExpanded
                        ) {
                            DocumentsContent(carId: car.id, documents: $editedCar.documents)
                        }
                    }
                    .padding(.horizontal, 16)

                    // Action buttons
                    ActionButtonsSection(
                        onEditLocation: { showEditLocation = true }
                    )
                    .padding(16)

                    // Danger zone
                    DangerZoneSection(
                        isPaused: editedCar.isPaused,
                        isRented: isCurrentlyRented,
                        isBusy: isTogglingPause,
                        onTogglePause: { Task { await togglePause() } },
                        onDelete: { showDeleteConfirmation = true }
                    )
                    .padding(.horizontal, 16)
                    .padding(.bottom, 100)
                }
            }

            // Bottom View/Edit toggle
            ViewEditToggle(isEditMode: $isEditMode)
                .padding(.horizontal, 16)
                .padding(.bottom, 16)
        }
        .background(Color(.systemBackground))
        .navigationTitle("Edit car")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .navigationBarTrailing) {
                saveStatusPill
            }
        }
        .onAppear {
            // Mark the view live AFTER the initial editedCar assignment has
            // settled. Without this guard, the synthetic onChange that fires
            // on first render would trigger a no-op autosave on entry.
            hasAppeared = true
        }
        .onChange(of: editedCar) { _, _ in
            // Any field edit, photo pick, or doc pick mutates editedCar.
            // Debounce so rapid keystrokes coalesce into one save.
            scheduleAutosave()
        }
        .onChange(of: isEditMode) { _, newValue in
            // Leaving edit mode — make sure nothing is lost. We flush the
            // pending debounce immediately. If a save is already in flight,
            // it'll re-schedule itself if more changes are detected.
            if newValue == false {
                autosaveTask?.cancel()
                autosaveTask = nil
                if saveState == .dirty {
                    Task { await performSave(triggeredByUser: false) }
                }
            }
        }
        .onDisappear {
            // Backstop in case the view leaves before edit-mode toggles
            // (e.g. swipe-back, programmatic dismiss). Fire-and-forget;
            // network request continues even after the view tears down.
            autosaveTask?.cancel()
            autosaveTask = nil
            if saveState == .dirty {
                Task { await performSave(triggeredByUser: false) }
            }
        }
        .sheet(isPresented: $showPhotosEditor) {
            CarPhotosEditView(photoSlots: $editedCar.photoSlots)
        }
        .fullScreenCover(isPresented: $showEditLocation, onDismiss: {
            // OwnerEditCarLocationView persists through PUT /cars/{id}/location
            // and refreshes the store — sync the local edit copy so this
            // screen shows the fresh location without re-entering (QA pt 9).
            if let updated = store.getCar(id: car.id) {
                editedCar.location = updated.location
            }
        }) {
            OwnerEditCarLocationView(car: editedCar)
        }
        .confirmationDialog(
            "Delete this car?",
            isPresented: $showDeleteConfirmation,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) {
                Task { await deleteCar() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This removes the listing from DrivaBai. Your rental history and chats are kept.")
        }
        .alert("Error", isPresented: $showErrorAlert) {
            Button("OK", role: .cancel) {
                errorMessage = nil
            }
        } message: {
            Text(errorMessage ?? "An unknown error occurred")
        }
        .disabled(isDeleting)
        .overlay {
            if isDeleting {
                ZStack {
                    Color.black.opacity(0.3)
                        .ignoresSafeArea()
                    VStack(spacing: 16) {
                        ProgressView()
                            .progressViewStyle(CircularProgressViewStyle(tint: .white))
                            .scaleEffect(1.5)
                        Text("Deleting...")
                            .foregroundColor(.white)
                            .font(.headline)
                    }
                    .padding(32)
                    .background(Color(.systemGray5).opacity(0.9))
                    .cornerRadius(16)
                }
            }
        }
    }

    // MARK: - Save-state UI

    /// Toolbar pill that surfaces the autosave state.
    ///   - .idle / .saved  → grey "Saved" check, no tap target
    ///   - .dirty / .saving → small spinner + "Saving…" label
    ///   - .failed(reason)  → red "Retry" button that re-invokes saveCar()
    /// We keep the Retry affordance on .failed so the user always has a
    /// deterministic way to recover from a transient save error.
    @ViewBuilder
    private var saveStatusPill: some View {
        switch saveState {
        case .idle, .saved:
            HStack(spacing: 4) {
                Image(systemName: "checkmark.circle.fill")
                Text("Saved")
            }
            .font(.subheadline.weight(.medium))
            .foregroundColor(.secondary)
            .accessibilityLabel("All changes saved")
        case .dirty, .saving:
            HStack(spacing: 6) {
                ProgressView().scaleEffect(0.75)
                Text("Saving…")
            }
            .font(.subheadline)
            .foregroundColor(.secondary)
            .accessibilityLabel("Saving changes")
        case .failed:
            Button {
                Task { await saveCar() }
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: "arrow.clockwise")
                    Text("Retry")
                }
                .font(.subheadline.weight(.semibold))
                .foregroundColor(.red)
            }
            .disabled(isSaving)
            .accessibilityLabel("Save failed — tap to retry")
        }
    }

    // MARK: - Actions

    /// Manual flush. Called by the toolbar status pill on the .failed branch
    /// (Retry) and by anything that needs a synchronous save (e.g. exiting
    /// edit mode). NOT called on every keystroke — autosave handles that.
    private func saveCar() async {
        // Cancel any pending debounce so we don't double-save.
        autosaveTask?.cancel()
        autosaveTask = nil
        await performSave(triggeredByUser: true)
    }

    /// Schedule a save after `debounceMillis` of no further edits. Subsequent
    /// edits restart the timer (autosaveTask is cancelled + replaced).
    /// While a save is in-flight, scheduling is a no-op — the in-flight
    /// save's post-condition Equatable check will pick up any change that
    /// happened during the flight and re-schedule itself.
    private func scheduleAutosave(debounceMillis: Int = 800) {
        guard hasAppeared else { return }
        if inFlightSave { return }
        saveState = .dirty
        autosaveTask?.cancel()
        autosaveTask = Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(debounceMillis))
            if Task.isCancelled { return }
            await performSave(triggeredByUser: false)
        }
    }

    /// One save round: PATCH the car attributes, then upload any freshly
    /// picked photos / documents and clear their local bytes on success.
    /// Re-runs itself if `editedCar` changed during the round so the user's
    /// last keystroke always wins.
    @MainActor
    private func performSave(triggeredByUser: Bool) async {
        if inFlightSave { return }
        inFlightSave = true
        saveState = .saving
        if triggeredByUser { isSaving = true }
        defer {
            inFlightSave = false
            if triggeredByUser { isSaving = false }
        }

        // Snapshot BEFORE the save so we can detect "user typed during the
        // network round-trip" and trigger a follow-up save.
        let snapshotBefore = editedCar

        // 1. Push any freshly-picked car documents FIRST — before the PATCH.
        // Sale-readiness validation on the attribute save requires a title
        // document to already be on file (QA pt 8), so "toggle for-sale +
        // stage a title" in one debounce window must upload the doc first.
        for idx in editedCar.documents.indices {
            guard editedCar.documents[idx].needsUpload,
                  let data = editedCar.documents[idx].localData
            else { continue }
            let doc = editedCar.documents[idx]
            let mime = doc.localMimeType ?? "application/octet-stream"
            let resp = await store.uploadDocument(
                carId: car.id,
                documentType: doc.documentType,
                data: data,
                filename: doc.filename,
                mimeType: mime
            )
            if let resp {
                // Adopt the server's identity for the uploaded row so later
                // View/Replace/Delete target the real document id and the
                // preview has a signed URL.
                editedCar.documents[idx] = CarDocument(
                    id: resp.id,
                    documentType: doc.documentType,
                    filename: resp.fileName,
                    fileSize: resp.fileSize,
                    uploadedAt: resp.createdAt,
                    fileURL: resp.fileUrl
                )
            } else {
                #if DEBUG
                print("[CarDetailEditView] Document upload failed: \(doc.documentType.rawValue) — \(store.error ?? "no error")")
                #endif
            }
        }

        // 2. Persist car attributes (PATCH). Idempotent, so a repeated save
        // with the same values is harmless. The backend ignores status /
        // is_paused / deposit here (QA pts 7/9) and may reject with
        // SALE_REQUIREMENTS_NOT_MET when for-sale requirements aren't met.
        guard let _ = await store.updateCar(editedCar) else {
            let reason = store.error ?? "Couldn't save"
            saveState = .failed(reason)
            if store.errorCode == "SALE_REQUIREMENTS_NOT_MET" {
                // Surface the backend validation message verbatim, inline
                // next to the For-sale checklist (QA pt 8).
                saleRequirementsMessage = reason
            }
            if triggeredByUser {
                errorMessage = reason
                showErrorAlert = true
            }
            return
        }
        saleRequirementsMessage = nil

        // 3. Push any freshly-picked photos. We mutate editedCar in place
        // to clear localImageData on success — this keeps the next autosave
        // a no-op for that slot (filter checks localImageData != nil).
        for idx in editedCar.photoSlots.indices {
            guard let data = editedCar.photoSlots[idx].localImageData else { continue }
            let slotType = editedCar.photoSlots[idx].slotType
            let ok = await store.uploadPhoto(data: data, carId: car.id, slotType: slotType)
            if ok {
                editedCar.photoSlots[idx].localImageData = nil
            } else {
                #if DEBUG
                print("[CarDetailEditView] Photo upload failed for slot \(slotType.rawValue)")
                #endif
            }
        }

        // 4. Did the user type / pick something new during the round-trip?
        // If yes, schedule another save without debounce delay (changes are
        // already old). If no, we're caught up.
        if editedCar != snapshotBefore {
            saveState = .dirty
            // No debounce — user changes are already "stable" from this
            // method's perspective. Re-fire on the next runloop tick.
            autosaveTask?.cancel()
            autosaveTask = Task { @MainActor in
                await performSave(triggeredByUser: false)
            }
        } else {
            saveState = .saved
        }
    }

    /// True while the car is held by a driver — pause is blocked (D2).
    private var isCurrentlyRented: Bool {
        editedCar.activeRental != nil || editedCar.status == .rented
    }

    /// Pause/resume through the dedicated POST /cars/{id}/pause endpoint
    /// (QA pt 9). The autosave PATCH no longer carries status/is_paused, so
    /// this is the only path that flips pause state — and the backend 409s
    /// with CAR_CURRENTLY_RENTED when a rental is running.
    private func togglePause() async {
        if isTogglingPause { return }

        // Client fast-path for the same rule the server enforces — skips a
        // round-trip that would 409 anyway.
        if isCurrentlyRented {
            errorMessage = "You can't pause a car during an active rental."
            showErrorAlert = true
            return
        }

        isTogglingPause = true
        defer { isTogglingPause = false }

        let ok = await store.togglePaused(id: car.id)
        if ok, let updated = store.getCar(id: car.id) {
            editedCar.isPaused = updated.isPaused
            editedCar.status = updated.status
        } else if !ok {
            if store.errorCode == "CAR_CURRENTLY_RENTED" {
                errorMessage = "You can't pause a car during an active rental."
            } else {
                errorMessage = store.error ?? "Couldn't update the listing."
            }
            showErrorAlert = true
        }
    }

    private func deleteCar() async {
        isDeleting = true

        let success = await store.deleteCar(id: car.id)

        isDeleting = false

        if success {
            // Success — the car is archived server-side and dropped from
            // the local list; pop back to My Cars.
            dismiss()
        } else if store.errorCode == "CAR_HAS_ACTIVE_OBLIGATIONS" {
            // Backend guard (QA pt 9 / D3): the car still has an active
            // rental, an open vehicle return / key handover, or a purchase
            // in progress.
            errorMessage = "This car can't be deleted yet — it has an active rental, an open vehicle return or key handover, or a purchase in progress. Wrap those up first, then try again."
            showErrorAlert = true
        } else {
            errorMessage = store.error ?? "Failed to delete car"
            showErrorAlert = true
        }
    }
}


// MARK: - Collapsible Section

struct CollapsibleSection<Content: View>: View {
    let title: String
    @Binding var isExpanded: Bool
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(spacing: 0) {
            // Header
            Button(action: { withAnimation(.easeInOut(duration: 0.2)) { isExpanded.toggle() } }) {
                HStack {
                    Text(title)
                        .font(.headline)
                        .foregroundColor(.primary)

                    Spacer()

                    Image(systemName: isExpanded ? "chevron.up" : "chevron.down")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                .padding(.vertical, 16)
            }
            .buttonStyle(PlainButtonStyle())

            // Content
            if isExpanded {
                content()
                    .padding(.bottom, 16)
            }
        }
    }
}

// MARK: - General Information Content

private struct GeneralInformationContent: View {
    @Binding var car: Car
    /// Verbatim SALE_REQUIREMENTS_NOT_MET message from the last rejected
    /// save, shown inline with the checklist (QA pt 8). Nil when the last
    /// save succeeded.
    let saleRequirementsMessage: String?

    var body: some View {
        VStack(spacing: 16) {
            // List for rent
            Toggle(isOn: $car.isForRent) {
                Text("List for rent")
                    .font(.subheadline)
                    .foregroundColor(.primary)
            }
            .tint(Color.driveBaiPrimary)

            // Weekly rent price
            if car.isForRent {
                PriceEditorRow(
                    label: "Weekly rent price",
                    suffix: "/ week",
                    value: Binding(
                        get: { car.weeklyRentPrice?.amount ?? 0 },
                        set: { car.weeklyRentPrice = Money(amount: $0) }
                    ),
                    minValue: kMinWeeklyRentPrice,
                    step: 10,
                    sheetTitle: "Weekly rent"
                )
            }

            // List for sale (QA pt 8) — owners can enable this any time;
            // the backend requires a sale price >= $1,000 and a Title
            // document before the save is accepted.
            Toggle(isOn: $car.isForSale) {
                Text("List for sale")
                    .font(.subheadline)
                    .foregroundColor(.primary)
            }
            .tint(Color.driveBaiPrimary)

            // Sale price + readiness checklist
            if car.isForSale {
                PriceEditorRow(
                    label: "Sale price",
                    suffix: nil,
                    value: Binding(
                        get: { car.salePrice?.amount ?? 0 },
                        set: { car.salePrice = Money(amount: $0) }
                    ),
                    minValue: 1000,
                    step: 10,
                    sheetTitle: "Sale price"
                )

                SaleReadinessChecklist(
                    car: car,
                    serverMessage: saleRequirementsMessage
                )
            }

            // Description
            VStack(alignment: .leading, spacing: 8) {
                Text("Description")
                    .font(.subheadline)
                    .foregroundColor(.secondary)

                TextEditor(text: $car.description)
                    .frame(minHeight: 100)
                    .padding(8)
                    .background(Color(.systemGray6))
                    .cornerRadius(8)
            }
        }
    }
}

// MARK: - Sale Readiness Checklist (QA pt 8)

/// Client-side mirror of the backend's SALE_REQUIREMENTS_NOT_MET rules:
/// a sale price of at least $1,000 and a Title document on file. Shown
/// only while something is missing (or when the server just rejected a
/// save), so a fully-ready listing renders no noise.
private struct SaleReadinessChecklist: View {
    let car: Car
    let serverMessage: String?

    private var priceOK: Bool { (car.salePrice?.amount ?? 0) >= 1000 }
    private var titleOK: Bool {
        car.documents.contains { $0.documentType == .title }
    }

    var body: some View {
        if !priceOK || !titleOK || serverMessage != nil {
            VStack(alignment: .leading, spacing: 8) {
                if let serverMessage {
                    // Backend validation error, verbatim.
                    Text(serverMessage)
                        .font(.caption.weight(.semibold))
                        .foregroundColor(.red)
                }

                Text("Required before this car can be listed for sale:")
                    .font(.caption)
                    .foregroundColor(.secondary)

                requirementRow(done: priceOK, text: "A sale price of at least $1,000")
                requirementRow(done: titleOK, text: "A Title document — add it in the Documents section below")
            }
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color.orange.opacity(0.08))
            .cornerRadius(8)
        }
    }

    private func requirementRow(done: Bool, text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: done ? "checkmark.circle.fill" : "circle")
                .font(.caption)
                .foregroundColor(done ? .green : .secondary)
            Text(text)
                .font(.caption)
                .foregroundColor(.primary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }
}

// MARK: - Core Features Content

private struct CoreFeaturesContent: View {
    @Binding var specs: CarSpecs

    var body: some View {
        VStack(spacing: 16) {
            // Mileage
            VStack(alignment: .leading, spacing: 8) {
                Text("Mileage")
                    .font(.subheadline)
                    .foregroundColor(.secondary)

                HStack {
                    TextField("Mileage", value: $specs.mileage, format: .number)
                        .keyboardType(.numberPad)
                        .textFieldStyle(.roundedBorder)

                    Text("mi")
                        .foregroundColor(.secondary)
                }
            }

            // Body type picker
            VStack(alignment: .leading, spacing: 8) {
                Text("Body type")
                    .font(.subheadline)
                    .foregroundColor(.secondary)

                Picker("Body type", selection: $specs.bodyType) {
                    ForEach(CarBodyType.allCases) { type in
                        Text(type.displayText).tag(type)
                    }
                }
                .pickerStyle(.menu)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
                .background(Color(.systemGray6))
                .cornerRadius(8)
            }

            // Fuel type picker
            VStack(alignment: .leading, spacing: 8) {
                Text("Fuel type")
                    .font(.subheadline)
                    .foregroundColor(.secondary)

                Picker("Fuel type", selection: $specs.fuelType) {
                    ForEach(FuelType.allCases) { type in
                        Text(type.rawValue).tag(type)
                    }
                }
                .pickerStyle(.menu)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
                .background(Color(.systemGray6))
                .cornerRadius(8)
            }
        }
    }
}

// MARK: - Requirements Content

private struct RequirementsContent: View {
    @Binding var requirements: CarRequirements

    var body: some View {
        VStack(spacing: 16) {
            // Years licensed
            VStack(alignment: .leading, spacing: 8) {
                Text("Minimum years licensed driving")
                    .font(.subheadline)
                    .foregroundColor(.secondary)

                Stepper(
                    "\(requirements.minYearsLicensedDriving) years",
                    value: $requirements.minYearsLicensedDriving,
                    in: 1...10
                )
                .padding(8)
                .background(Color(.systemGray6))
                .cornerRadius(8)
            }

            // Deposit field removed (QA pt 7) — deposits never entered any
            // payment formula and are no longer part of the product; the
            // backend ignores the value and serves 0.

            // Insurance coverage
            VStack(alignment: .leading, spacing: 8) {
                Text("Minimum driver insurance")
                    .font(.subheadline)
                    .foregroundColor(.secondary)

                Picker("Insurance", selection: $requirements.insuranceCoverage) {
                    ForEach(InsuranceCoverage.allCases) { coverage in
                        Text(coverage.displayText).tag(coverage)
                    }
                }
                .pickerStyle(.segmented)
            }
        }
    }
}

// MARK: - Documents Content

/// Server-backed documents section (QA pts 8/11): lists the car's documents
/// with type/filename, tap-to-preview via `DocumentPreviewSheet` (signed
/// URLs, refreshed on section expand), Replace via the shared
/// `DocumentSourcePicker` (camera / library / files — QA pt 1) and a real
/// backend Delete for uploaded rows.
private struct DocumentsContent: View {
    let carId: UUID
    @Binding var documents: [CarDocument]

    @ObservedObject private var store = OwnerCarsStore.shared

    @State private var showingSourcePicker = false
    @State private var showingAddDocumentSheet = false
    @State private var selectedDocumentType: CarDocumentType?
    @State private var documentToReplace: CarDocument?
    @State private var showDeleteConfirmation = false
    @State private var documentToDelete: CarDocument?
    @State private var previewDocument: CarDocument?
    @State private var deleteErrorMessage: String?
    @State private var isDeletingDocument = false

    /// Fresh signed URLs fetched when the section expands. The URLs embedded
    /// in the car payload are signed per-response and may have gone stale by
    /// the time the user opens this section; GET /cars/{id}/documents
    /// re-signs them. Keyed by document id and used only for previews, so
    /// the refetch never dirties `documents` (which would trigger a
    /// pointless autosave round).
    @State private var freshFileURLs: [UUID: String] = [:]

    var body: some View {
        VStack(spacing: 12) {
            ForEach(documents) { doc in
                DocumentRow(
                    document: doc,
                    canPreview: previewSource(for: doc) != nil,
                    onView: { previewDocument = doc },
                    onReplace: {
                        documentToReplace = doc
                        selectedDocumentType = doc.documentType
                        showingSourcePicker = true
                    },
                    onDelete: {
                        documentToDelete = doc
                        showDeleteConfirmation = true
                    }
                )
            }

            // Add document button
            Button(action: { showingAddDocumentSheet = true }) {
                HStack {
                    Image(systemName: "plus.circle.fill")
                    Text("Add document")
                }
                .font(.subheadline)
                .foregroundColor(Color.driveBaiPrimary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.top, 8)
        }
        .disabled(isDeletingDocument)
        .task { await refreshSignedURLs() }
        .sheet(isPresented: $showingAddDocumentSheet) {
            AddDocumentTypeSheet(
                existingTypes: Set(documents.map { $0.documentType }),
                onSelect: { type in
                    selectedDocumentType = type
                    documentToReplace = nil
                    showingAddDocumentSheet = false
                    showingSourcePicker = true
                }
            )
        }
        .documentSourcePicker(
            isPresented: $showingSourcePicker,
            filenameBase: selectedDocumentType?.rawValue ?? "document"
        ) { picked in
            addOrReplaceDocument(picked)
        }
        .sheet(item: $previewDocument) { doc in
            if let source = previewSource(for: doc) {
                DocumentPreviewSheet(source: source)
            }
        }
        .confirmationDialog(
            "Delete document?",
            isPresented: $showDeleteConfirmation,
            titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) {
                if let doc = documentToDelete {
                    deleteDocument(doc)
                }
            }
            Button("Cancel", role: .cancel) {
                documentToDelete = nil
            }
        } message: {
            Text("This will remove the document from your listing.")
        }
        .alert("Couldn't delete document", isPresented: Binding(
            get: { deleteErrorMessage != nil },
            set: { if !$0 { deleteErrorMessage = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(deleteErrorMessage ?? "")
        }
    }

    // MARK: Preview

    /// Best available preview source: staged local bytes first, then the
    /// freshest signed URL we know about. Nil → nothing to preview yet.
    private func previewSource(for doc: CarDocument) -> DocumentPreviewSheet.Source? {
        if let data = doc.localData {
            return .localData(data, filename: doc.filename, mimeType: doc.localMimeType)
        }
        if let url = freshFileURLs[doc.id] ?? doc.fileURL {
            return .remoteURL(url, filename: doc.filename)
        }
        return nil
    }

    @MainActor
    private func refreshSignedURLs() async {
        do {
            let docs = try await APIClient.shared.fetchCarDocuments(carId: carId)
            var map: [UUID: String] = [:]
            for doc in docs {
                map[doc.id] = doc.fileUrl
            }
            freshFileURLs = map
        } catch {
            // Non-fatal: previews fall back to the payload-embedded URL.
            #if DEBUG
            print("[DocumentsContent] fetchCarDocuments failed: \(error)")
            #endif
        }
    }

    // MARK: Add / Replace / Delete

    private func addOrReplaceDocument(_ picked: PickedDocument) {
        guard let type = selectedDocumentType else { return }

        let newDocument = CarDocument(
            documentType: type,
            filename: picked.filename,
            fileSize: picked.data.count,
            uploadedAt: Date(),
            localData: picked.data,
            localMimeType: picked.mimeType
        )

        if let replaceDoc = documentToReplace,
           let index = documents.firstIndex(where: { $0.id == replaceDoc.id }) {
            documents[index] = newDocument
        } else {
            documents.append(newDocument)
        }

        // Reset state
        selectedDocumentType = nil
        documentToReplace = nil
    }

    /// Staged-only docs are dropped locally; uploaded docs are deleted on
    /// the backend first and only removed from the list when the server
    /// allowed it (QA pt 8 "delete-if-allowed").
    private func deleteDocument(_ document: CarDocument) {
        if document.needsUpload && document.fileURL == nil {
            documents.removeAll { $0.id == document.id }
            documentToDelete = nil
            return
        }

        isDeletingDocument = true
        Task { @MainActor in
            let ok = await store.deleteDocument(carId: carId, documentId: document.id)
            if ok {
                documents.removeAll { $0.id == document.id }
                freshFileURLs[document.id] = nil
            } else {
                deleteErrorMessage = store.error ?? "The document couldn't be deleted. Please try again."
            }
            documentToDelete = nil
            isDeletingDocument = false
        }
    }
}

// MARK: - Document Row

private struct DocumentRow: View {
    let document: CarDocument
    let canPreview: Bool
    let onView: () -> Void
    let onReplace: () -> Void
    let onDelete: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: document.documentType.iconName)
                .font(.title3)
                .foregroundColor(Color.driveBaiPrimary)
                .frame(width: 32)

            VStack(alignment: .leading, spacing: 2) {
                Text(document.documentType.displayText)
                    .font(.subheadline)
                    .fontWeight(.medium)
                    .foregroundColor(.primary)

                Text(subtitleText)
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .lineLimit(1)
            }

            Spacer()

            Menu {
                Button(action: onView) {
                    Label("View", systemImage: "eye")
                }
                .disabled(!canPreview)
                Button(action: onReplace) {
                    Label("Replace", systemImage: "arrow.triangle.2.circlepath")
                }
                Button(role: .destructive, action: onDelete) {
                    Label("Delete", systemImage: "trash")
                }
            } label: {
                Image(systemName: "ellipsis")
                    .foregroundColor(.secondary)
                    .frame(width: 32, height: 32)
            }
        }
        .padding(12)
        .background(Color(.systemGray6))
        .cornerRadius(8)
        .contentShape(Rectangle())
        .onTapGesture {
            // Row tap = preview (QA pt 11). No-op when there's nothing to
            // show yet (e.g. legacy rows without a URL).
            if canPreview { onView() }
        }
    }

    private var subtitleText: String {
        var text = "\(document.filename) · \(document.fileSizeFormatted)"
        if document.needsUpload {
            text += " · not uploaded yet"
        }
        return text
    }
}

// MARK: - Add Document Type Sheet

private struct AddDocumentTypeSheet: View {
    let existingTypes: Set<CarDocumentType>
    let onSelect: (CarDocumentType) -> Void
    @Environment(\.dismiss) private var dismiss

    private var availableTypes: [CarDocumentType] {
        CarDocumentType.allCases.filter { !existingTypes.contains($0) }
    }

    var body: some View {
        NavigationStack {
            List {
                if availableTypes.isEmpty {
                    Text("All document types have been added")
                        .foregroundColor(.secondary)
                } else {
                    ForEach(availableTypes) { type in
                        Button(action: { onSelect(type) }) {
                            HStack(spacing: 12) {
                                Image(systemName: type.iconName)
                                    .font(.title3)
                                    .foregroundColor(Color.driveBaiPrimary)
                                    .frame(width: 32)

                                Text(type.displayText)
                                    .foregroundColor(.primary)

                                Spacer()

                                Image(systemName: "chevron.right")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                            }
                        }
                    }
                }
            }
            .navigationTitle("Select Document Type")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium])
    }
}

// MARK: - Action Buttons Section

private struct ActionButtonsSection: View {
    let onEditLocation: () -> Void

    var body: some View {
        VStack(spacing: 12) {
            // QA pt 9c: opens the real 3-step location flow
            // (OwnerEditCarLocationView), persisted via PUT /cars/{id}/location.
            ActionButton(
                title: "Edit car location",
                iconName: "location.fill",
                action: onEditLocation
            )

            // "Manage unavailable dates" removed (QA pt 9d / D6): there is
            // no availability backend this round; the dead row was the bug.
        }
    }
}

private struct ActionButton: View {
    let title: String
    let iconName: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack {
                Image(systemName: iconName)
                    .foregroundColor(Color.driveBaiPrimary)

                Text(title)
                    .foregroundColor(.primary)

                Spacer()

                Image(systemName: "chevron.right")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .padding(16)
            .background(Color(.systemGray6))
            .cornerRadius(12)
        }
        .buttonStyle(PlainButtonStyle())
    }
}

// MARK: - Danger Zone Section

private struct DangerZoneSection: View {
    let isPaused: Bool
    /// True while a rental is running — pausing is blocked (server 409
    /// CAR_CURRENTLY_RENTED; QA pt 9a / D2). The button stays tappable so
    /// the user gets an explanation instead of a dead control.
    let isRented: Bool
    /// True while the POST /pause round-trip is in flight.
    let isBusy: Bool
    let onTogglePause: () -> Void
    let onDelete: () -> Void

    var body: some View {
        VStack(spacing: 12) {
            // Pause/Resume button
            Button(action: onTogglePause) {
                HStack {
                    if isBusy {
                        ProgressView().scaleEffect(0.8)
                    } else {
                        Image(systemName: isPaused ? "play.fill" : "pause.fill")
                    }
                    Text(isPaused ? "Resume listing" : "Pause listing")
                }
                .font(.subheadline)
                .fontWeight(.medium)
                .foregroundColor(.orange)
                .frame(maxWidth: .infinity)
                .padding(16)
                .background(Color.orange.opacity(0.1))
                .cornerRadius(12)
            }
            .buttonStyle(PlainButtonStyle())
            .disabled(isBusy)

            if isRented {
                Text("Pausing is unavailable while this car is on an active rental.")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }

            // Delete button
            Button(action: onDelete) {
                HStack {
                    Image(systemName: "trash.fill")
                    Text("Delete")
                }
                .font(.subheadline)
                .fontWeight(.medium)
                .foregroundColor(.red)
                .frame(maxWidth: .infinity)
                .padding(16)
                .background(Color.red.opacity(0.1))
                .cornerRadius(12)
            }
            .buttonStyle(PlainButtonStyle())
        }
    }
}

#Preview {
    NavigationStack {
        CarDetailEditView(
            car: OwnerCarsStore.shared.cars.first!,
            isEditMode: .constant(true)
        )
    }
}
