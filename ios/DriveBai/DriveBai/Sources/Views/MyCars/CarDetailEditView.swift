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

    // Loading and error state
    @State private var isSaving: Bool = false
    @State private var isDeleting: Bool = false
    @State private var errorMessage: String?
    @State private var showErrorAlert: Bool = false

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
                            GeneralInformationContent(car: $editedCar)
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
                            DocumentsContent(documents: $editedCar.documents)
                        }
                    }
                    .padding(.horizontal, 16)

                    // Action buttons
                    ActionButtonsSection(
                        onResetLocation: resetLocation,
                        onSetUnavailableDates: setUnavailableDates
                    )
                    .padding(16)

                    // Danger zone
                    DangerZoneSection(
                        isPaused: editedCar.isPaused,
                        onTogglePause: togglePause,
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
            Text("This action cannot be undone.")
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

        // 1. Persist car attributes (PATCH). Idempotent, so a repeated save
        // with the same values is harmless.
        guard let _ = await store.updateCar(editedCar) else {
            let reason = store.error ?? "Couldn't save"
            saveState = .failed(reason)
            if triggeredByUser {
                errorMessage = reason
                showErrorAlert = true
            }
            return
        }

        // 2. Push any freshly-picked photos. We mutate editedCar in place
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

        // 3. Push any freshly-picked car documents. Same clear-on-success
        // pattern so the next autosave doesn't re-upload identical bytes.
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
            if resp != nil {
                editedCar.documents[idx].localData = nil
                editedCar.documents[idx].localMimeType = nil
            } else {
                #if DEBUG
                print("[CarDetailEditView] Document upload failed: \(doc.documentType.rawValue) — \(store.error ?? "no error")")
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

    private func resetLocation() {
        // Would open location picker
    }

    private func setUnavailableDates() {
        // Would open date picker
    }

    private func togglePause() {
        editedCar.isPaused.toggle()
        editedCar.status = editedCar.isPaused ? .paused : .available
    }

    private func deleteCar() async {
        isDeleting = true

        let success = await store.deleteCar(id: car.id)

        isDeleting = false

        if success {
            // Success - dismiss back to list
            dismiss()
        } else {
            // Show error
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

    var body: some View {
        VStack(spacing: 16) {
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

            // Sale price
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

private struct PriceSliderField: View {
    let label: String
    @Binding var value: Double
    let range: ClosedRange<Double>
    let step: Double
    let suffix: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(label)
                    .font(.subheadline)
                    .foregroundColor(.secondary)

                Spacer()

                HStack(spacing: 2) {
                    Text(Money(amount: value).formatted)
                        .font(.subheadline)
                        .fontWeight(.semibold)
                        .foregroundColor(.primary)

                    if let suffix = suffix {
                        Text(suffix)
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }
            }

            Slider(value: $value, in: range, step: step)
                .tint(Color.driveBaiPrimary)
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

            // Deposit amount
            PriceSliderField(
                label: "Deposit amount",
                value: Binding(
                    get: { requirements.depositAmount.amount },
                    set: { requirements.depositAmount = Money(amount: $0) }
                ),
                range: 100...5000,
                step: 50,
                suffix: nil
            )

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

private struct DocumentsContent: View {
    @Binding var documents: [CarDocument]

    @State private var showingSourceChooser = false
    @State private var showingFilePicker = false
    @State private var showingPhotoPicker = false
    @State private var showingAddDocumentSheet = false
    @State private var selectedDocumentType: CarDocumentType?
    @State private var documentToReplace: CarDocument?
    @State private var showDeleteConfirmation = false
    @State private var documentToDelete: CarDocument?
    @State private var pickerItem: PhotosPickerItem?

    var body: some View {
        VStack(spacing: 12) {
            ForEach(documents) { doc in
                DocumentRow(
                    document: doc,
                    onReplace: {
                        documentToReplace = doc
                        selectedDocumentType = doc.documentType
                        showingSourceChooser = true
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
        .sheet(isPresented: $showingAddDocumentSheet) {
            AddDocumentTypeSheet(
                existingTypes: Set(documents.map { $0.documentType }),
                onSelect: { type in
                    selectedDocumentType = type
                    documentToReplace = nil
                    showingAddDocumentSheet = false
                    showingSourceChooser = true
                }
            )
        }
        .confirmationDialog("Upload Document", isPresented: $showingSourceChooser, titleVisibility: .visible) {
            Button("Photo Library") { showingPhotoPicker = true }
            Button("Files") { showingFilePicker = true }
            Button("Cancel", role: .cancel) {}
        }
        .photosPicker(isPresented: $showingPhotoPicker, selection: $pickerItem, matching: .images)
        .onChange(of: pickerItem) { _, newItem in
            guard let item = newItem else { return }
            pickerItem = nil
            Task {
                guard let type = selectedDocumentType else { return }
                guard let data = try? await item.loadTransferable(type: Data.self) else { return }
                let ext: String
                let mimeType: String
                if let contentType = item.supportedContentTypes.first, contentType.conforms(to: .png) {
                    ext = "png"
                    mimeType = "image/png"
                } else {
                    ext = "jpg"
                    mimeType = "image/jpeg"
                }
                let filename = "\(type.rawValue).\(ext)"
                await MainActor.run {
                    addOrReplaceDocument(
                        type: type,
                        filename: filename,
                        fileSize: data.count,
                        data: data,
                        mimeType: mimeType
                    )
                }
            }
        }
        .fileImporter(
            isPresented: $showingFilePicker,
            allowedContentTypes: [.pdf, .jpeg, .png, .image],
            allowsMultipleSelection: false
        ) { result in
            handleDocumentImport(result: result)
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
    }

    private func handleDocumentImport(result: Result<[URL], Error>) {
        guard let type = selectedDocumentType else { return }

        switch result {
        case .success(let urls):
            guard let url = urls.first else { return }
            guard url.startAccessingSecurityScopedResource() else { return }
            defer { url.stopAccessingSecurityScopedResource() }

            // Actually read the file bytes — the previous code only read the
            // filename + size and threw the contents away, which silently
            // turned every car-document upload into a local-only no-op.
            let data: Data
            do {
                data = try Data(contentsOf: url)
            } catch {
                #if DEBUG
                print("[DocumentsContent] Read failed: \(error)")
                #endif
                return
            }

            let filename = url.lastPathComponent
            let mimeType = mimeTypeForExtension(url.pathExtension.lowercased())

            addOrReplaceDocument(
                type: type,
                filename: filename,
                fileSize: data.count,
                data: data,
                mimeType: mimeType
            )

        case .failure(let error):
            #if DEBUG
            print("[DocumentsContent] Document import failed: \(error)")
            #endif
        }
    }

    /// Maps a file extension to a Content-Type the backend whitelists.
    /// Falls back to application/octet-stream for unknown types — the
    /// server's CarDocumentType is what actually determines categorization,
    /// the MIME is just for the HTTP request shape.
    private func mimeTypeForExtension(_ ext: String) -> String {
        switch ext {
        case "pdf": return "application/pdf"
        case "jpg", "jpeg": return "image/jpeg"
        case "png": return "image/png"
        case "heic": return "image/heic"
        default: return "application/octet-stream"
        }
    }

    private func addOrReplaceDocument(
        type: CarDocumentType,
        filename: String,
        fileSize: Int,
        data: Data,
        mimeType: String
    ) {
        let newDocument = CarDocument(
            documentType: type,
            filename: filename,
            fileSize: fileSize,
            uploadedAt: Date(),
            localData: data,
            localMimeType: mimeType
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

    private func deleteDocument(_ document: CarDocument) {
        documents.removeAll { $0.id == document.id }
        documentToDelete = nil
    }
}

// MARK: - Document Row

private struct DocumentRow: View {
    let document: CarDocument
    let onReplace: () -> Void
    let onDelete: () -> Void

    @State private var showingMenu = false

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

                Text("\(document.filename) · \(document.fileSizeFormatted)")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            Spacer()

            Menu {
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
    let onResetLocation: () -> Void
    let onSetUnavailableDates: () -> Void

    var body: some View {
        VStack(spacing: 12) {
            ActionButton(
                title: "Reset car location",
                iconName: "location.fill",
                action: onResetLocation
            )

            ActionButton(
                title: "Manage unavailable dates",
                iconName: "calendar",
                action: onSetUnavailableDates
            )
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
    let onTogglePause: () -> Void
    let onDelete: () -> Void

    var body: some View {
        VStack(spacing: 12) {
            // Pause/Resume button
            Button(action: onTogglePause) {
                HStack {
                    Image(systemName: isPaused ? "play.fill" : "pause.fill")
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
