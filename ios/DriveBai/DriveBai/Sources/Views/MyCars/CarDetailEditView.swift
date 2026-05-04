import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

// Minimum weekly rent price: 1 in debug builds (testing), 50 in release (production).
#if DEBUG
private let kMinWeeklyRentPrice: Double = 1
#else
private let kMinWeeklyRentPrice: Double = 50
#endif

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
                Button {
                    Task { await saveCar() }
                } label: {
                    if isSaving {
                        ProgressView()
                            .progressViewStyle(CircularProgressViewStyle())
                    } else {
                        Text("Save")
                            .fontWeight(.semibold)
                    }
                }
                .foregroundColor(Color.driveBaiPrimary)
                .disabled(isSaving || isDeleting)
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

    // MARK: - Actions

    private func saveCar() async {
        isSaving = true

        // First update the car data
        let result = await store.updateCar(editedCar)

        if result == nil {
            isSaving = false
            errorMessage = store.error ?? "Failed to save changes"
            showErrorAlert = true
            return
        }

        // Then upload any new photos (photos with localImageData but no imageURL, or localImageData changed)
        let photosToUpload = editedCar.photoSlots.filter { slot in
            // Upload if there's local data and either no existing URL or it's a new selection
            slot.localImageData != nil
        }

        for slot in photosToUpload {
            if let imageData = slot.localImageData {
                let success = await store.uploadPhoto(
                    data: imageData,
                    carId: car.id,
                    slotType: slot.slotType
                )
                if !success {
                    print("[CarDetailEditView] Failed to upload photo for slot: \(slot.slotType.rawValue)")
                }
            }
        }

        isSaving = false
        // Success - exit edit mode
        isEditMode = false
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
                Text("Insurance coverage required")
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
                if let contentType = item.supportedContentTypes.first, contentType.conforms(to: .png) {
                    ext = "png"
                } else {
                    ext = "jpg"
                }
                let filename = "\(type.rawValue).\(ext)"
                await MainActor.run {
                    addOrReplaceDocument(type: type, filename: filename, fileSize: data.count)
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

            let filename = url.lastPathComponent
            let fileSize: Int
            if let attributes = try? FileManager.default.attributesOfItem(atPath: url.path),
               let size = attributes[.size] as? Int {
                fileSize = size
            } else {
                fileSize = 0
            }

            addOrReplaceDocument(type: type, filename: filename, fileSize: fileSize)

        case .failure(let error):
            #if DEBUG
            print("[DocumentsContent] Document import failed: \(error)")
            #endif
        }
    }

    private func addOrReplaceDocument(type: CarDocumentType, filename: String, fileSize: Int) {
        let newDocument = CarDocument(
            documentType: type,
            filename: filename,
            fileSize: fileSize,
            uploadedAt: Date()
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
                title: "Set/change unavailable dates",
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
