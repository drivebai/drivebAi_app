import SwiftUI
import PhotosUI
import CoreLocation
import UniformTypeIdentifiers

// Minimum weekly rent price: 1 in debug builds (testing), 50 in release (production).
#if DEBUG
private let kMinWeeklyRentPrice: Double = 1
#else
private let kMinWeeklyRentPrice: Double = 50
#endif

// MARK: - Create Listing Flow State

enum CreateListingStep: Int, CaseIterable {
    case basicInfo
    case details
    case pricing
    case requirements
    case photos
    case documents
    case review

    var title: String {
        switch self {
        case .basicInfo: return "Basic Info"
        case .details: return "Car Details"
        case .pricing: return "Pricing"
        case .requirements: return "Requirements"
        case .photos: return "Photos"
        case .documents: return "Vehicle Documents"
        case .review: return "Review"
        }
    }
}

// MARK: - Pending Car Document Slot
//
// In-flight container for a document the user picks during the wizard.
// We hold the bytes locally and upload them only after the car is created
// (POST /cars/{carId}/documents) — same lifecycle as photo slots.
struct PendingCarDocument: Identifiable, Equatable {
    let id = UUID()
    let type: CarDocumentType
    var filename: String
    var fileSize: Int
    var data: Data
    var mimeType: String
}

@MainActor
class CreateListingState: ObservableObject {
    // Basic Info
    @Published var vin: String = ""
    @Published var make: String = ""
    @Published var model: String = ""
    @Published var year: Int = Calendar.current.component(.year, from: Date())

    // VIN decode state
    @Published var isDecodingVIN: Bool = false
    @Published var vinDecodeError: String?
    @Published var vinDecodeWarning: String?
    @Published var vinDecodeSucceeded: Bool = false

    // Car Details
    @Published var bodyType: CarBodyType = .sedan
    @Published var fuelType: FuelType = .gas
    @Published var mileage: Int = 0

    // Pricing
    @Published var isForRent: Bool = true
    @Published var weeklyRentPrice: Double = 350
    @Published var isForSale: Bool = false
    @Published var salePrice: Double = 25000

    // Requirements
    @Published var minYearsLicensed: Int = 2
    @Published var depositAmount: Double = 500
    @Published var insuranceCoverage: InsuranceCoverage = .fullCoverage

    // Location
    @Published var neighborhood: String = ""
    @Published var locationLatitude: Double = 0
    @Published var locationLongitude: Double = 0
    @Published var locationArea: String = ""
    @Published var locationStreet: String = ""
    @Published var locationBlock: String = ""
    @Published var locationZip: String = ""
    @Published var locationAddressLine: String = ""

    var hasSelectedLocation: Bool {
        locationLatitude != 0 && locationLongitude != 0
    }

    var locationCoordinate: CLLocationCoordinate2D? {
        guard hasSelectedLocation else { return nil }
        return CLLocationCoordinate2D(latitude: locationLatitude, longitude: locationLongitude)
    }

    var locationDisplaySummary: String {
        if !locationAddressLine.isEmpty { return locationAddressLine }
        if !locationArea.isEmpty { return locationArea }
        return "Not set"
    }

    // Description
    @Published var description: String = ""

    // Photos
    @Published var photoSlots: [CarPhotoSlot] = Car.createEmptyPhotoSlots()

    // Documents (optional; uploaded after createCar succeeds)
    // Keyed by CarDocumentType so the user can only stage one per slot —
    // a Replace overwrites the entry, a Remove deletes it.
    @Published var pendingDocuments: [CarDocumentType: PendingCarDocument] = [:]

    // Non-blocking warning shown after the listing is created if any of
    // the document uploads failed. Photo of the car was created OK; we
    // just couldn't attach the docs.
    @Published var docUploadWarning: String?

    // Navigation
    @Published var currentStep: CreateListingStep = .basicInfo
    @Published var isNavigatingForward: Bool = true
    @Published var isLoading: Bool = false
    @Published var error: String?

    /// True when the latest store error is the "VIN already in use" duplicate
    /// conflict. Used by the BasicInfo step to render the message inline
    /// under the VIN field and by Review to color it appropriately.
    var isVINConflictError: Bool {
        guard let error = error else { return false }
        return error.localizedCaseInsensitiveContains("vin already in use")
    }

    // Computed
    var currentStepIndex: Int {
        currentStep.rawValue + 1
    }

    var totalSteps: Int {
        CreateListingStep.allCases.count
    }

    var isBasicInfoValid: Bool {
        !make.trimmingCharacters(in: .whitespaces).isEmpty &&
        !model.trimmingCharacters(in: .whitespaces).isEmpty &&
        year >= 1990 && year <= Calendar.current.component(.year, from: Date()) + 1
    }

    var isDetailsValid: Bool {
        mileage >= 0
    }

    var isPricingValid: Bool {
        (isForRent || isForSale) &&
        (!isForRent || weeklyRentPrice >= kMinWeeklyRentPrice) &&
        (!isForSale || salePrice >= 1000)
    }

    var isRequirementsValid: Bool {
        minYearsLicensed >= 0 && depositAmount >= 0
    }

    var hasAtLeastOnePhoto: Bool {
        photoSlots.contains { $0.hasImage }
    }

    var displayTitle: String {
        "\(year) \(make) \(model)"
    }

    // Navigation
    func goToNextStep() {
        isNavigatingForward = true
        if let nextIndex = CreateListingStep(rawValue: currentStep.rawValue + 1) {
            currentStep = nextIndex
        }
    }

    func goToPreviousStep() {
        isNavigatingForward = false
        if let prevIndex = CreateListingStep(rawValue: currentStep.rawValue - 1) {
            currentStep = prevIndex
        }
    }

    func clearError() {
        error = nil
    }

    // MARK: - VIN

    /// Trimmed + uppercased view of the VIN field. Cheap to compute, used
    /// for validation + the network call. We never mutate `vin` itself so
    /// the user keeps whatever they typed (mixed-case is allowed in the
    /// field; the API call normalizes).
    var normalizedVIN: String {
        vin.trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
    }

    /// SAE J853 VIN shape check: exactly 17 alphanumeric chars, excluding
    /// I/O/Q (they're forbidden to avoid 1/0 confusion). Same predicate the
    /// backend enforces — we re-check it here so the "Search" button only
    /// lights up when the value can plausibly succeed upstream.
    var isVINShapeValid: Bool {
        let v = normalizedVIN
        guard v.count == 17 else { return false }
        let allowed: Set<Character> = Set("0123456789ABCDEFGHJKLMNPRSTUVWXYZ")
        return v.allSatisfy { allowed.contains($0) }
    }

    /// Fetches decoded fields from the backend's NHTSA proxy and autofills
    /// whatever the upstream knew. Never clobbers Model when NHTSA omits it
    /// (older VINs frequently come back without a Model string) — that
    /// avoids surprising the user with empty fields after an apparently-
    /// successful decode.
    func decodeVIN(using apiClient: APIClient = .shared) async {
        guard !isDecodingVIN else { return }
        guard isVINShapeValid else {
            vinDecodeError = "VIN must be 17 characters, no I/O/Q."
            vinDecodeSucceeded = false
            return
        }
        isDecodingVIN = true
        vinDecodeError = nil
        vinDecodeWarning = nil
        defer { isDecodingVIN = false }

        do {
            let resp = try await apiClient.decodeVIN(normalizedVIN)
            // Persist the normalized VIN so future edits keep the canonical form.
            vin = resp.vin
            if let m = resp.make, !m.isEmpty { make = m }
            if let m = resp.model, !m.isEmpty { model = m }
            if let y = resp.year, y > 1989, y <= Calendar.current.component(.year, from: Date()) + 1 {
                year = y
            }
            if let body = resp.bodyType, !body.isEmpty,
               let mapped = CarBodyType.allCases.first(where: { $0.rawValue.lowercased() == body.lowercased() }) {
                bodyType = mapped
            }
            if let fuel = resp.fuelType, !fuel.isEmpty,
               let mapped = FuelType.allCases.first(where: { $0.rawValue.lowercased() == fuel.lowercased() }) {
                fuelType = mapped
            }
            vinDecodeSucceeded = true
            vinDecodeWarning = resp.warning?.isEmpty == false ? resp.warning : nil
        } catch let APIError.serverError(_, message) {
            vinDecodeError = message
            vinDecodeSucceeded = false
        } catch {
            vinDecodeError = "Couldn't decode that VIN. Please try again or enter details manually."
            vinDecodeSucceeded = false
        }
    }

    // Create Car
    func createCar(ownerId: UUID, ownerName: String) -> Car {
        let normalizedVIN = vin.trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
        let specs = CarSpecs(
            bodyType: bodyType,
            fuelType: fuelType,
            mileage: mileage,
            year: year,
            make: make,
            model: model,
            vin: normalizedVIN.isEmpty ? nil : normalizedVIN
        )

        let requirements = CarRequirements(
            minYearsLicensedDriving: minYearsLicensed,
            depositAmount: Money(amount: depositAmount),
            insuranceCoverage: insuranceCoverage
        )

        let location = CarLocation(
            address: locationAddressLine,
            neighborhood: locationArea.isEmpty ? neighborhood : locationArea,
            distanceMiles: 0,
            latitude: locationLatitude,
            longitude: locationLongitude,
            area: locationArea,
            street: locationStreet,
            block: locationBlock,
            zip: locationZip
        )

        let owner = CarOwnerInfo(
            id: ownerId,
            name: ownerName,
            avatarURL: nil,
            rating: 5.0,
            reviewCount: 0
        )

        return Car(
            title: displayTitle,
            description: description.isEmpty ? "No description provided." : description,
            specs: specs,
            requirements: requirements,
            location: location,
            owner: owner,
            isForRent: isForRent,
            weeklyRentPrice: isForRent ? Money(amount: weeklyRentPrice) : nil,
            isForSale: isForSale,
            salePrice: isForSale ? Money(amount: salePrice) : nil,
            status: .pending,
            photoSlots: photoSlots,
            documents: []
        )
    }
}

// MARK: - Create Listing Flow View

struct CreateListingFlowView: View {
    @Environment(\.dismiss) private var dismiss
    @StateObject private var state = CreateListingState()
    @StateObject private var store = OwnerCarsStore.shared
    @StateObject private var authStore = AuthStore.shared

    private var stepTransition: AnyTransition {
        .asymmetric(
            insertion: .move(edge: state.isNavigatingForward ? .trailing : .leading),
            removal: .move(edge: state.isNavigatingForward ? .leading : .trailing)
        )
    }

    var body: some View {
        NavigationStack {
            ZStack {
                Group {
                    switch state.currentStep {
                    case .basicInfo:
                        CreateListingBasicInfoStep()
                            .transition(stepTransition)
                    case .details:
                        CreateListingDetailsStep()
                            .transition(stepTransition)
                    case .pricing:
                        CreateListingPricingStep()
                            .transition(stepTransition)
                    case .requirements:
                        CreateListingRequirementsStep()
                            .transition(stepTransition)
                    case .photos:
                        CreateListingPhotosStep()
                            .transition(stepTransition)
                    case .documents:
                        CreateListingDocumentsStep()
                            .transition(stepTransition)
                    case .review:
                        CreateListingReviewStep(onSubmit: submitListing)
                            .transition(stepTransition)
                    }
                }
                .animation(.easeInOut(duration: 0.3), value: state.currentStep)
            }
            .navigationTitle("New Listing")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .environmentObject(state)
        .interactiveDismissDisabled(state.isLoading)
        .alert(
            "Listing created",
            isPresented: Binding(
                get: { state.docUploadWarning != nil },
                set: { newValue in
                    if !newValue {
                        state.docUploadWarning = nil
                        dismiss()
                    }
                }
            ),
            actions: {
                Button("OK", role: .cancel) {}
            },
            message: {
                Text(state.docUploadWarning ?? "")
            }
        )
    }

    private func submitListing() {
        Task {
            await submitListingAsync()
        }
    }

    private func submitListingAsync() async {
        state.isLoading = true
        state.clearError()

        print("[CreateListingFlow] Starting listing submission...")

        // Get current user from auth state
        guard let user = authStore.state.user else {
            print("[CreateListingFlow] ERROR: No authenticated user found")
            state.error = "You must be logged in to create a listing"
            state.isLoading = false
            return
        }

        print("[CreateListingFlow] User authenticated: \(user.fullName) (ID: \(user.id))")

        // Create the car with actual user info
        let newCar = state.createCar(
            ownerId: user.id,
            ownerName: user.fullName
        )

        print("[CreateListingFlow] Created car model: \(newCar.title)")

        // Create car on backend
        guard let createdCar = await store.addCar(newCar) else {
            print("[CreateListingFlow] ERROR: Failed to create car on backend")
            print("[CreateListingFlow] Store error: \(store.error ?? "nil")")
            state.error = store.error ?? "Failed to create listing"
            state.isLoading = false
            return
        }

        print("[CreateListingFlow] Car created on backend with ID: \(createdCar.id)")

        // Upload photos for slots that have local image data
        let photosToUpload = state.photoSlots.filter { $0.localImageData != nil }
        print("[CreateListingFlow] Found \(photosToUpload.count) photos to upload")

        for slot in photosToUpload {
            if let imageData = slot.localImageData {
                print("[CreateListingFlow] Uploading photo for slot: \(slot.slotType.rawValue), size: \(imageData.count) bytes")
                let success = await store.uploadPhoto(
                    data: imageData,
                    carId: createdCar.id,
                    slotType: slot.slotType
                )
                if success {
                    print("[CreateListingFlow] Successfully uploaded photo for slot: \(slot.slotType.rawValue)")
                } else {
                    print("[CreateListingFlow] FAILED to upload photo for slot: \(slot.slotType.rawValue)")
                }
            }
        }

        // Upload any documents the user staged in the Documents step. Each
        // failure is non-fatal — the car is already created; we collect
        // them and surface a single warning toast at the end so the user
        // can retry uploads from the car detail screen.
        let docsToUpload = Array(state.pendingDocuments.values)
        if !docsToUpload.isEmpty {
            print("[CreateListingFlow] Found \(docsToUpload.count) documents to upload")
            var failedDocs: [CarDocumentType] = []
            for doc in docsToUpload {
                print("[CreateListingFlow] Uploading document: \(doc.type.rawValue), size: \(doc.data.count) bytes")
                let response = await store.uploadDocument(
                    carId: createdCar.id,
                    documentType: doc.type,
                    data: doc.data,
                    filename: doc.filename,
                    mimeType: doc.mimeType
                )
                if response != nil {
                    print("[CreateListingFlow] Successfully uploaded document: \(doc.type.rawValue)")
                } else {
                    print("[CreateListingFlow] FAILED to upload document: \(doc.type.rawValue): \(store.error ?? "nil")")
                    failedDocs.append(doc.type)
                }
            }
            if !failedDocs.isEmpty {
                let names = failedDocs.map { $0.displayText }.joined(separator: ", ")
                state.docUploadWarning = "Couldn't attach: \(names). You can add them later from your car detail."
            }
        }

        // Refresh the car from backend to get the updated status and photo URLs
        // This ensures the UI shows the correct state immediately (including "available" status
        // which is set automatically when cover photo is uploaded)
        print("[CreateListingFlow] Refreshing car from backend...")
        await store.refreshCar(id: createdCar.id)

        // Verify the refresh worked by checking the car's state in the store
        #if DEBUG
        if let refreshedCar = store.getCar(id: createdCar.id) {
            print("[CreateListingFlow] Refresh verification:")
            print("  - Car ID: \(refreshedCar.id)")
            print("  - Status: \(refreshedCar.status.rawValue)")
            print("  - isForRent: \(refreshedCar.isForRent)")
            print("  - isPaused: \(refreshedCar.isPaused)")
            let coverSlot = refreshedCar.photoSlots.first { $0.slotType == .coverFront }
            print("  - Cover photo URL: \(coverSlot?.imageURL ?? "nil")")
            print("  - Cover photo fullURL: \(coverSlot?.fullImageURL?.absoluteString ?? "nil")")
        } else {
            print("[CreateListingFlow] ERROR: Car not found in store after refresh!")
        }
        print("[CreateListingFlow] Store now has \(store.cars.count) cars")
        print("[CreateListingFlow] carsForRent count: \(store.carsForRent.count)")
        #endif

        print("[CreateListingFlow] Listing submission complete!")
        state.isLoading = false

        // If any docs failed to upload, hold the flow open with a warning
        // so the user knows the car was created but the docs didn't stick.
        // Tapping "OK" dismisses; the warning is non-blocking — the listing
        // is already on the server.
        if state.docUploadWarning == nil {
            dismiss()
        }
    }
}

// MARK: - Step Container

struct CreateListingStepContainer<Content: View>: View {
    let title: String
    let subtitle: String?
    let currentStep: Int
    let totalSteps: Int
    let canContinue: Bool
    let continueTitle: String
    let showBack: Bool
    let onBack: (() -> Void)?
    let onContinue: () -> Void
    @ViewBuilder let content: () -> Content

    init(
        title: String,
        subtitle: String? = nil,
        currentStep: Int,
        totalSteps: Int,
        canContinue: Bool,
        continueTitle: String = "Continue",
        showBack: Bool = true,
        onBack: (() -> Void)? = nil,
        onContinue: @escaping () -> Void,
        @ViewBuilder content: @escaping () -> Content
    ) {
        self.title = title
        self.subtitle = subtitle
        self.currentStep = currentStep
        self.totalSteps = totalSteps
        self.canContinue = canContinue
        self.continueTitle = continueTitle
        self.showBack = showBack
        self.onBack = onBack
        self.onContinue = onContinue
        self.content = content
    }

    var body: some View {
        VStack(spacing: 0) {
            // Progress indicator
            HStack(spacing: 4) {
                ForEach(1...totalSteps, id: \.self) { step in
                    Rectangle()
                        .fill(step <= currentStep ? Color.driveBaiPrimary : Color(.systemGray4))
                        .frame(height: 4)
                        .cornerRadius(2)
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 8)

            // Header
            VStack(alignment: .leading, spacing: 8) {
                Text(title)
                    .font(.title2)
                    .fontWeight(.bold)

                if let subtitle = subtitle {
                    Text(subtitle)
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16)
            .padding(.top, 24)

            // Content
            ScrollView {
                content()
                    .padding(.horizontal, 16)
                    .padding(.top, 24)
            }

            // Bottom buttons
            VStack(spacing: 12) {
                Button(action: onContinue) {
                    Text(continueTitle)
                        .font(.headline)
                        .foregroundColor(.white)
                        .frame(maxWidth: .infinity)
                        .padding(16)
                        .background(canContinue ? Color.driveBaiPrimary : Color.gray)
                        .cornerRadius(12)
                }
                .disabled(!canContinue)

                if showBack, let onBack = onBack {
                    Button(action: onBack) {
                        Text("Back")
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                    }
                }
            }
            .padding(16)
        }
        .background(Color(.systemBackground))
    }
}

// MARK: - Step 1: Basic Info

struct CreateListingBasicInfoStep: View {
    @EnvironmentObject private var state: CreateListingState
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        CreateListingStepContainer(
            title: "What car are you listing?",
            subtitle: "Enter the basic information about your vehicle",
            currentStep: state.currentStepIndex,
            totalSteps: state.totalSteps,
            canContinue: state.isBasicInfoValid,
            showBack: true,
            onBack: { dismiss() },
            onContinue: { state.goToNextStep() }
        ) {
            VStack(spacing: 20) {
                // VIN + Search — optional shortcut. If lookup succeeds we
                // autofill Make / Model / Year (and later steps pre-fill
                // Body / Fuel). User can still edit any of them afterwards.
                VINAutofillSection()

                // VIN-conflict surface: if addCar() bubbled a 409 about a
                // duplicate VIN, show it right under the VIN row so the
                // user fixes the field without scrolling to Review.
                if let error = state.error, state.isVINConflictError {
                    HStack(spacing: 6) {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundColor(.red)
                        Text(error)
                            .font(.caption)
                            .foregroundColor(.red)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }

                // Make
                VStack(alignment: .leading, spacing: 8) {
                    Text("Make")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    TextField("e.g. Toyota, Honda, BMW", text: $state.make)
                        .textFieldStyle(.roundedBorder)
                }

                // Model
                VStack(alignment: .leading, spacing: 8) {
                    Text("Model")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    TextField("e.g. Camry, Accord, X5", text: $state.model)
                        .textFieldStyle(.roundedBorder)
                }

                // Year
                VStack(alignment: .leading, spacing: 8) {
                    Text("Year")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    Picker("Year", selection: $state.year) {
                        ForEach((1990...Calendar.current.component(.year, from: Date()) + 1).reversed(), id: \.self) { year in
                            Text(String(year)).tag(year)
                        }
                    }
                    .pickerStyle(.wheel)
                    .frame(height: 120)
                }
            }
        }
    }
}

// MARK: - VIN Autofill Section

/// Optional VIN field + Search button. Lives at the top of Step 1 because
/// hitting Search populates Make / Model / Year / Body / Fuel in one go,
/// turning the rest of the wizard into review-and-tweak instead of typing
/// everything from scratch.
private struct VINAutofillSection: View {
    @EnvironmentObject private var state: CreateListingState

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Text("VIN")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                Text("optional")
                    .font(.caption)
                    .foregroundColor(.secondary.opacity(0.7))
                Spacer()
                if state.vinDecodeSucceeded && state.vinDecodeError == nil {
                    HStack(spacing: 4) {
                        Image(systemName: "checkmark.circle.fill")
                        Text("Autofilled")
                    }
                    .font(.caption.weight(.medium))
                    .foregroundColor(.green)
                }
            }

            HStack(spacing: 8) {
                TextField("17-character VIN", text: $state.vin)
                    .textFieldStyle(.roundedBorder)
                    .textInputAutocapitalization(.characters)
                    .autocorrectionDisabled(true)
                    .onChange(of: state.vin) { _, _ in
                        // Any keystroke invalidates a prior decode result so
                        // the "Autofilled" pill doesn't lie about a stale VIN.
                        state.vinDecodeSucceeded = false
                        state.vinDecodeError = nil
                        state.vinDecodeWarning = nil
                        // Clear a stale "VIN already in use" conflict so the
                        // inline banner disappears as soon as the user starts
                        // typing a different value.
                        if state.isVINConflictError {
                            state.clearError()
                        }
                    }

                Button {
                    Task { await state.decodeVIN() }
                } label: {
                    Group {
                        if state.isDecodingVIN {
                            ProgressView()
                                .progressViewStyle(.circular)
                                .tint(.white)
                        } else {
                            Text("Search")
                                .font(.subheadline.weight(.semibold))
                        }
                    }
                    .frame(minWidth: 72, minHeight: 30)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(state.isVINShapeValid && !state.isDecodingVIN ? Color.driveBaiPrimary : Color(.systemGray3))
                    .foregroundColor(.white)
                    .cornerRadius(8)
                }
                .accessibilityLabel("Search VIN")
                .disabled(!state.isVINShapeValid || state.isDecodingVIN)
            }

            if let error = state.vinDecodeError {
                Text(error)
                    .font(.caption)
                    .foregroundColor(.red)
            } else if let warning = state.vinDecodeWarning {
                Text(warning)
                    .font(.caption)
                    .foregroundColor(.orange)
            } else {
                Text("Enter your VIN to autofill make, model, year, and more.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
        }
    }
}

// MARK: - Step 2: Details

struct CreateListingDetailsStep: View {
    @EnvironmentObject private var state: CreateListingState

    var body: some View {
        CreateListingStepContainer(
            title: "Car Details",
            subtitle: "Tell us more about your \(state.displayTitle)",
            currentStep: state.currentStepIndex,
            totalSteps: state.totalSteps,
            canContinue: state.isDetailsValid,
            onBack: { state.goToPreviousStep() },
            onContinue: { state.goToNextStep() }
        ) {
            VStack(spacing: 24) {
                // Body Type - Dropdown Menu
                DropdownField(
                    label: "Body Type",
                    selection: $state.bodyType,
                    options: CarBodyType.allCases,
                    displayText: { $0.displayText }
                )

                // Fuel Type - Dropdown Menu
                DropdownField(
                    label: "Fuel Type",
                    selection: $state.fuelType,
                    options: FuelType.allCases,
                    displayText: { $0.rawValue }
                )

                // Mileage
                VStack(alignment: .leading, spacing: 8) {
                    Text("Current Mileage")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    HStack {
                        TextField("Mileage", value: $state.mileage, format: .number)
                            .keyboardType(.numberPad)
                            .textFieldStyle(.roundedBorder)

                        Text("miles")
                            .foregroundColor(.secondary)
                    }
                }

                // Description
                VStack(alignment: .leading, spacing: 8) {
                    Text("Description (optional)")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    TextEditor(text: $state.description)
                        .frame(minHeight: 100)
                        .padding(8)
                        .background(Color(.systemGray6))
                        .cornerRadius(8)
                }
            }
        }
    }
}

// MARK: - Dropdown Field Component

/// A dropdown-style form field that looks like an input with a chevron
struct DropdownField<T: Hashable & Identifiable>: View {
    let label: String
    @Binding var selection: T
    let options: [T]
    let displayText: (T) -> String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(label)
                .font(.subheadline)
                .foregroundColor(.secondary)

            Menu {
                ForEach(options) { option in
                    Button(action: {
                        selection = option
                    }) {
                        HStack {
                            Text(displayText(option))
                            if option.id == selection.id {
                                Image(systemName: "checkmark")
                            }
                        }
                    }
                }
            } label: {
                HStack {
                    Text(displayText(selection))
                        .foregroundColor(.primary)
                    Spacer()
                    Image(systemName: "chevron.down")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 14)
                .background(Color(.systemGray6))
                .cornerRadius(8)
            }
        }
    }
}

// MARK: - Step 3: Pricing

struct CreateListingPricingStep: View {
    @EnvironmentObject private var state: CreateListingState

    var body: some View {
        CreateListingStepContainer(
            title: "Your Weekly Rent & Sale Price",
            subtitle: "Choose how you want to offer your car",
            currentStep: state.currentStepIndex,
            totalSteps: state.totalSteps,
            canContinue: state.isPricingValid,
            onBack: { state.goToPreviousStep() },
            onContinue: { state.goToNextStep() }
        ) {
            VStack(spacing: 24) {
                // For Rent Toggle
                Toggle(isOn: $state.isForRent) {
                    HStack {
                        Image(systemName: "key.fill")
                            .foregroundColor(Color.driveBaiPrimary)
                        Text("Available for Rent")
                            .fontWeight(.medium)
                    }
                }
                .tint(Color.driveBaiPrimary)

                if state.isForRent {
                    VStack(alignment: .leading, spacing: 8) {
                        PriceEditorRow(
                            label: "Weekly Rent Price",
                            suffix: "/ week",
                            value: $state.weeklyRentPrice,
                            minValue: kMinWeeklyRentPrice,
                            step: 10,
                            sheetTitle: "Weekly rent"
                        )

                        Text("Minimum $\(Int(kMinWeeklyRentPrice)) — tap to edit with +/- or type")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }

                Divider()

                // For Sale Toggle
                Toggle(isOn: $state.isForSale) {
                    HStack {
                        Image(systemName: "cart.fill")
                            .foregroundColor(Color.driveBaiPrimary)
                        Text("Available for Sale")
                            .fontWeight(.medium)
                    }
                }
                .tint(Color.driveBaiPrimary)

                if state.isForSale {
                    VStack(alignment: .leading, spacing: 8) {
                        PriceEditorRow(
                            label: "Sale Price",
                            suffix: nil,
                            value: $state.salePrice,
                            minValue: 1000,
                            step: 10,
                            sheetTitle: "Sale price"
                        )

                        Text("Minimum $1,000 — tap to edit with +/- or type")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }

                if !state.isForRent && !state.isForSale {
                    Text("Please select at least one option")
                        .font(.caption)
                        .foregroundColor(.red)
                }
            }
        }
    }
}

// MARK: - Step 4: Requirements

struct CreateListingRequirementsStep: View {
    @EnvironmentObject private var state: CreateListingState
    @State private var showLocationPicker = false

    var body: some View {
        CreateListingStepContainer(
            title: "Renter Requirements",
            subtitle: "Who can rent this car",
            currentStep: state.currentStepIndex,
            totalSteps: state.totalSteps,
            canContinue: state.isRequirementsValid,
            onBack: { state.goToPreviousStep() },
            onContinue: { state.goToNextStep() }
        ) {
            VStack(spacing: 24) {
                // Minimum years licensed
                VStack(alignment: .leading, spacing: 8) {
                    Text("Minimum Years Licensed")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    Stepper(
                        "\(state.minYearsLicensed) years",
                        value: $state.minYearsLicensed,
                        in: 0...10
                    )
                    .padding()
                    .background(Color(.systemGray6))
                    .cornerRadius(8)
                }

                // Deposit amount
                VStack(alignment: .leading, spacing: 8) {
                    Text("Security Deposit")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    CurrencyTextField(value: $state.depositAmount, placeholder: "0")
                }

                // Insurance coverage
                VStack(alignment: .leading, spacing: 8) {
                    Text("Minimum driver insurance")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    Picker("Insurance", selection: $state.insuranceCoverage) {
                        ForEach(InsuranceCoverage.allCases) { coverage in
                            Text(coverage.displayText).tag(coverage)
                        }
                    }
                    .pickerStyle(.segmented)
                }

                // Pickup location - map picker row
                VStack(alignment: .leading, spacing: 8) {
                    Text("Pickup Location")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    Button(action: { showLocationPicker = true }) {
                        HStack(spacing: 12) {
                            Image(systemName: "mappin.circle.fill")
                                .font(.system(size: 24))
                                .foregroundColor(Color.driveBaiPrimary)

                            VStack(alignment: .leading, spacing: 2) {
                                if state.hasSelectedLocation {
                                    Text(state.locationDisplaySummary)
                                        .font(.subheadline)
                                        .fontWeight(.medium)
                                        .foregroundColor(.primary)
                                        .lineLimit(2)
                                        .multilineTextAlignment(.leading)
                                } else {
                                    Text("Choose on map")
                                        .font(.subheadline)
                                        .foregroundColor(.secondary)
                                }
                            }

                            Spacer()

                            Image(systemName: "chevron.right")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                        .padding(12)
                        .background(Color(.systemGray6))
                        .cornerRadius(10)
                        .overlay(
                            RoundedRectangle(cornerRadius: 10)
                                .stroke(Color(.systemGray4), lineWidth: 1)
                        )
                    }
                    .buttonStyle(PlainButtonStyle())
                }
            }
        }
        .sheet(isPresented: $showLocationPicker) {
            CarPickupLocationPickerView(
                initialCoordinate: state.locationCoordinate,
                onSave: { coordinate, address in
                    state.locationLatitude = coordinate.latitude
                    state.locationLongitude = coordinate.longitude
                    state.locationArea = address.area
                    state.locationStreet = address.street
                    state.locationBlock = address.block
                    state.locationZip = address.zip
                    state.locationAddressLine = address.addressLine
                    state.neighborhood = address.area
                }
            )
        }
    }
}

// MARK: - Step 5: Photos

struct CreateListingPhotosStep: View {
    @EnvironmentObject private var state: CreateListingState

    // Multi-select picker state
    @State private var selectedItems: [PhotosPickerItem] = []
    @State private var isLoadingBatch: Bool = false
    @State private var batchMessage: String?

    // Grid configuration for consistent photo tiles
    private let columns = [
        GridItem(.flexible(), spacing: 12),
        GridItem(.flexible(), spacing: 12)
    ]
    private let tileCornerRadius: CGFloat = 16
    private let tileAspectRatio: CGFloat = 1.4

    private var emptySlotCount: Int {
        state.photoSlots.filter { !$0.hasImage }.count
    }

    var body: some View {
        CreateListingStepContainer(
            title: "Add Photos",
            subtitle: "Photos help renters see your car",
            currentStep: state.currentStepIndex,
            totalSteps: state.totalSteps,
            canContinue: true, // Photos are optional
            continueTitle: state.hasAtLeastOnePhoto ? "Continue" : "Skip for now",
            onBack: { state.goToPreviousStep() },
            onContinue: { state.goToNextStep() }
        ) {
            VStack(spacing: 16) {
                // Multi-select picker button
                if emptySlotCount > 0 {
                    PhotosPicker(
                        selection: $selectedItems,
                        maxSelectionCount: emptySlotCount,
                        matching: .images
                    ) {
                        HStack(spacing: 8) {
                            if isLoadingBatch {
                                ProgressView()
                                    .tint(.white)
                            } else {
                                Image(systemName: "photo.on.rectangle.angled")
                                    .font(.system(size: 16, weight: .semibold))
                            }
                            Text(isLoadingBatch ? "Loading photos..." : "Select Multiple Photos")
                                .fontWeight(.semibold)
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 14)
                        .background(Color.driveBaiPrimary)
                        .foregroundColor(.white)
                        .cornerRadius(12)
                    }
                    .disabled(isLoadingBatch)
                    .onChange(of: selectedItems) { _, newItems in
                        guard !newItems.isEmpty else { return }
                        handleBatchSelection(newItems)
                    }
                } else {
                    Text("All photo slots are filled. Tap a photo to replace it.")
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .multilineTextAlignment(.center)
                        .padding(.vertical, 8)
                }

                // Batch message (auto-dismiss)
                if let message = batchMessage {
                    HStack(spacing: 6) {
                        Image(systemName: "info.circle.fill")
                            .font(.subheadline)
                            .foregroundColor(.driveBaiPrimary)
                        Text(message)
                            .font(.subheadline)
                            .foregroundColor(.secondary)
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(Color.driveBaiPrimary.opacity(0.08))
                    .cornerRadius(8)
                }

                // Photo slots grid — each slot still has its own picker for individual replacement
                LazyVGrid(columns: columns, spacing: 12) {
                    ForEach(Array(state.photoSlots.enumerated()), id: \.element.id) { index, slot in
                        IndependentPhotoSlotPicker(
                            slot: slot,
                            cornerRadius: tileCornerRadius,
                            aspectRatio: tileAspectRatio,
                            onPhotoSelected: { imageData in
                                #if DEBUG
                                print("[CreateListingPhotosStep] Photo selected for slot: \(slot.slotType.rawValue), data size: \(imageData.count) bytes")
                                #endif
                                state.photoSlots[index].localImageData = imageData
                            }
                        )
                    }
                }
            }
        }
    }

    private func handleBatchSelection(_ items: [PhotosPickerItem]) {
        isLoadingBatch = true
        batchMessage = nil

        Task {
            var loadedImages: [Data] = []
            for item in items {
                do {
                    if let data = try await item.loadTransferable(type: Data.self) {
                        loadedImages.append(data)
                    }
                } catch {
                    #if DEBUG
                    print("[CreateListingPhotosStep] Failed to load image from picker: \(error)")
                    #endif
                }
            }

            await MainActor.run {
                // Find empty slot indices in sort order
                let emptyIndices = state.photoSlots.enumerated()
                    .filter { !$0.element.hasImage }
                    .map { $0.offset }

                let assignCount = min(loadedImages.count, emptyIndices.count)
                for i in 0..<assignCount {
                    state.photoSlots[emptyIndices[i]].localImageData = loadedImages[i]
                }

                #if DEBUG
                print("[CreateListingPhotosStep] Batch: \(items.count) selected, \(loadedImages.count) loaded, \(assignCount) assigned")
                #endif

                if loadedImages.count > emptyIndices.count {
                    let extras = loadedImages.count - emptyIndices.count
                    batchMessage = "\(assignCount) photos added. \(extras) skipped — no empty slots."
                } else if assignCount > 0 {
                    batchMessage = "\(assignCount) photo\(assignCount == 1 ? "" : "s") added."
                } else if loadedImages.isEmpty {
                    batchMessage = "Could not load the selected photos."
                }

                isLoadingBatch = false
                selectedItems = [] // Reset so picker can be used again

                // Auto-dismiss message after 4 seconds
                if batchMessage != nil {
                    Task {
                        try? await Task.sleep(nanoseconds: 4_000_000_000)
                        await MainActor.run {
                            batchMessage = nil
                        }
                    }
                }
            }
        }
    }
}

/// Each photo slot has its own independent PhotosPicker to avoid shared state issues.
/// This fixes the bug where selecting images for multiple slots would fail or show same image.
struct IndependentPhotoSlotPicker: View {
    let slot: CarPhotoSlot
    var cornerRadius: CGFloat = 16
    var aspectRatio: CGFloat = 1.4
    let onPhotoSelected: (Data) -> Void

    // Each slot has its OWN picker item state - this is the key fix
    @State private var pickerItem: PhotosPickerItem?
    @State private var isLoading: Bool = false

    var body: some View {
        VStack(spacing: 8) {
            PhotosPicker(
                selection: $pickerItem,
                matching: .images
            ) {
                tileContent
                    .aspectRatio(aspectRatio, contentMode: .fit)
                    .frame(maxWidth: .infinity)
                    .clipShape(RoundedRectangle(cornerRadius: cornerRadius))
                    .overlay(
                        RoundedRectangle(cornerRadius: cornerRadius)
                            .stroke(Color(.systemGray4), lineWidth: 1)
                    )
            }
            .buttonStyle(PlainButtonStyle())
            .onChange(of: pickerItem) { _, newItem in
                guard let newItem = newItem else { return }
                loadPhoto(from: newItem)
            }

            // Label below the tile
            Text(slot.slotType.displayLabel)
                .font(.subheadline)
                .fontWeight(.semibold)
                .foregroundColor(.primary)
                .multilineTextAlignment(.center)
                .frame(height: 20)
                .frame(maxWidth: .infinity)
        }
    }

    @ViewBuilder
    private var tileContent: some View {
        GeometryReader { geometry in
            ZStack {
                if let data = slot.localImageData, let uiImage = UIImage(data: data) {
                    // Local image (newly selected)
                    Image(uiImage: uiImage)
                        .resizable()
                        .scaledToFill()
                        .frame(width: geometry.size.width, height: geometry.size.height)
                        .clipped()
                } else if let fullURL = slot.fullImageURL {
                    // Remote image from server — shared ImagePipeline.
                    RemoteImage(url: fullURL, contentMode: .fill, maxPixelSize: 800)
                        .frame(width: geometry.size.width, height: geometry.size.height)
                        .clipped()
                } else {
                    // Empty state
                    emptyPlaceholder
                }

                // Loading overlay
                if isLoading {
                    Color.black.opacity(0.4)
                    ProgressView()
                        .tint(.white)
                        .scaleEffect(1.2)
                }
            }
        }
    }

    private var emptyPlaceholder: some View {
        Rectangle()
            .fill(Color(.systemGray6))
            .overlay(
                VStack(spacing: 8) {
                    Image(systemName: "camera.fill")
                        .font(.system(size: 28))
                        .foregroundColor(Color(.systemGray3))

                    Image(systemName: "plus.circle.fill")
                        .font(.system(size: 18))
                        .foregroundColor(Color.driveBaiPrimary)
                }
            )
    }

    private var loadingPlaceholder: some View {
        Rectangle()
            .fill(Color(.systemGray6))
            .overlay(
                ProgressView()
                    .tint(Color.driveBaiPrimary)
            )
    }

    private func loadPhoto(from item: PhotosPickerItem) {
        isLoading = true
        #if DEBUG
        print("[IndependentPhotoSlotPicker] Loading photo for slot: \(slot.slotType.rawValue)")
        #endif

        Task {
            do {
                if let data = try await item.loadTransferable(type: Data.self) {
                    await MainActor.run {
                        #if DEBUG
                        print("[IndependentPhotoSlotPicker] Loaded \(data.count) bytes for slot: \(slot.slotType.rawValue)")
                        #endif
                        onPhotoSelected(data)
                        isLoading = false
                    }
                } else {
                    #if DEBUG
                    print("[IndependentPhotoSlotPicker] Failed to load data for slot: \(slot.slotType.rawValue) - nil data")
                    #endif
                    await MainActor.run {
                        isLoading = false
                    }
                }
            } catch {
                #if DEBUG
                print("[IndependentPhotoSlotPicker] Error loading photo for slot: \(slot.slotType.rawValue) - \(error)")
                #endif
                await MainActor.run {
                    isLoading = false
                }
            }
        }
    }
}

// MARK: - Step 6: Vehicle Documents

/// Optional documents step. Users can stage up to one of each
/// CarDocumentType (registration, insurance, inspection, permit). All
/// uploads are deferred until after the car is created in the final
/// submit step; failures there are non-fatal.
struct CreateListingDocumentsStep: View {
    @EnvironmentObject private var state: CreateListingState

    var body: some View {
        CreateListingStepContainer(
            title: "Vehicle Documents",
            subtitle: "Optional — speeds up admin review.",
            currentStep: state.currentStepIndex,
            totalSteps: state.totalSteps,
            canContinue: true, // Documents are always optional
            continueTitle: state.pendingDocuments.isEmpty ? "Skip for now" : "Continue",
            onBack: { state.goToPreviousStep() },
            onContinue: { state.goToNextStep() }
        ) {
            VStack(alignment: .leading, spacing: 16) {
                ForEach(CarDocumentType.allCases) { type in
                    PendingDocumentSlotCard(type: type)
                }

                Text("Documents help admin review faster — you can also add them later from your car detail.")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .padding(.top, 4)
            }
        }
    }
}

/// One row per document type. Shows an Upload control when nothing is
/// staged, switches to a filename + Replace/Remove menu after the user
/// picks a file (photo or PDF).
private struct PendingDocumentSlotCard: View {
    let type: CarDocumentType
    @EnvironmentObject private var state: CreateListingState

    @State private var pickerItem: PhotosPickerItem?
    @State private var showingSourceChooser = false
    @State private var showingPhotoPicker = false
    @State private var showingFilePicker = false

    private var pending: PendingCarDocument? {
        state.pendingDocuments[type]
    }

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: type.iconName)
                .font(.title3)
                .foregroundColor(Color.driveBaiPrimary)
                .frame(width: 32)

            VStack(alignment: .leading, spacing: 2) {
                Text(type.displayText)
                    .font(.subheadline)
                    .fontWeight(.medium)
                    .foregroundColor(.primary)

                if let pending = pending {
                    Text("\(pending.filename) · \(byteCountFormatted(pending.fileSize))")
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                } else {
                    Text("Optional")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
            }

            Spacer()

            if pending == nil {
                Button {
                    showingSourceChooser = true
                } label: {
                    Text("Upload")
                        .font(.subheadline.weight(.semibold))
                        .foregroundColor(.white)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 6)
                        .background(Color.driveBaiPrimary)
                        .cornerRadius(8)
                }
                .buttonStyle(.plain)
            } else {
                Menu {
                    Button {
                        showingSourceChooser = true
                    } label: {
                        Label("Replace", systemImage: "arrow.triangle.2.circlepath")
                    }
                    Button(role: .destructive) {
                        state.pendingDocuments[type] = nil
                    } label: {
                        Label("Remove", systemImage: "trash")
                    }
                } label: {
                    Image(systemName: "ellipsis")
                        .foregroundColor(.secondary)
                        .frame(width: 32, height: 32)
                }
            }
        }
        .padding(12)
        .background(Color(.systemGray6))
        .cornerRadius(10)
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
                    state.pendingDocuments[type] = PendingCarDocument(
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
            switch result {
            case .success(let urls):
                guard let url = urls.first else { return }
                guard url.startAccessingSecurityScopedResource() else { return }
                defer { url.stopAccessingSecurityScopedResource() }
                guard let data = try? Data(contentsOf: url) else { return }
                let filename = url.lastPathComponent
                let mimeType = mimeTypeForExtension(url.pathExtension.lowercased())
                state.pendingDocuments[type] = PendingCarDocument(
                    type: type,
                    filename: filename,
                    fileSize: data.count,
                    data: data,
                    mimeType: mimeType
                )
            case .failure:
                break
            }
        }
    }

    private func byteCountFormatted(_ bytes: Int) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(bytes), countStyle: .file)
    }

    private func mimeTypeForExtension(_ ext: String) -> String {
        switch ext {
        case "pdf": return "application/pdf"
        case "jpg", "jpeg": return "image/jpeg"
        case "png": return "image/png"
        case "heic": return "image/heic"
        default: return "application/octet-stream"
        }
    }
}

// MARK: - Step 7: Review

struct CreateListingReviewStep: View {
    @EnvironmentObject private var state: CreateListingState
    let onSubmit: () -> Void

    var body: some View {
        CreateListingStepContainer(
            title: "Review Your Listing",
            subtitle: "Make sure everything looks good",
            currentStep: state.currentStepIndex,
            totalSteps: state.totalSteps,
            canContinue: true,
            continueTitle: state.isLoading ? "Creating..." : "Create Listing",
            onBack: { state.goToPreviousStep() },
            onContinue: onSubmit
        ) {
            VStack(alignment: .leading, spacing: 20) {
                // Title
                Text(state.displayTitle)
                    .font(.title2)
                    .fontWeight(.bold)

                // Details card
                ReviewSection(title: "Details") {
                    ReviewRow(label: "Body Type", value: state.bodyType.displayText)
                    ReviewRow(label: "Fuel Type", value: state.fuelType.rawValue)
                    ReviewRow(label: "Mileage", value: "\(state.mileage) miles")
                }

                // Pricing card
                ReviewSection(title: "Pricing") {
                    if state.isForRent {
                        ReviewRow(label: "Rent", value: "\(Money(amount: state.weeklyRentPrice).formatted) / week")
                    }
                    if state.isForSale {
                        ReviewRow(label: "Sale", value: Money(amount: state.salePrice).formatted)
                    }
                }

                // Requirements card
                ReviewSection(title: "Requirements") {
                    ReviewRow(label: "Min. Years Licensed", value: "\(state.minYearsLicensed) years")
                    ReviewRow(label: "Deposit", value: Money(amount: state.depositAmount).formatted)
                    ReviewRow(label: "Insurance", value: state.insuranceCoverage.displayText)
                }

                // Location card
                ReviewSection(title: "Pickup Location") {
                    if state.hasSelectedLocation {
                        ReviewRow(label: "Location", value: state.locationDisplaySummary)
                    } else {
                        ReviewRow(label: "Location", value: "Not set")
                    }
                }

                // Photos preview
                if state.hasAtLeastOnePhoto {
                    ReviewSection(title: "Photos") {
                        ScrollView(.horizontal, showsIndicators: false) {
                            HStack(spacing: 8) {
                                ForEach(state.photoSlots.filter { $0.hasImage }) { slot in
                                    if let data = slot.localImageData,
                                       let uiImage = UIImage(data: data) {
                                        Image(uiImage: uiImage)
                                            .resizable()
                                            .scaledToFill()
                                            .frame(width: 80, height: 60)
                                            .clipShape(RoundedRectangle(cornerRadius: 8))
                                    }
                                }
                            }
                        }
                    }
                }

                if let error = state.error {
                    Text(error)
                        .font(.caption)
                        .foregroundColor(.red)
                }
            }
        }
    }
}

struct ReviewSection<Content: View>: View {
    let title: String
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .font(.headline)
                .foregroundColor(.primary)

            VStack(spacing: 4) {
                content()
            }
            .padding(12)
            .background(Color(.systemGray6))
            .cornerRadius(8)
        }
    }
}

struct ReviewRow: View {
    let label: String
    let value: String

    var body: some View {
        HStack {
            Text(label)
                .font(.subheadline)
                .foregroundColor(.secondary)
            Spacer()
            Text(value)
                .font(.subheadline)
                .fontWeight(.medium)
        }
    }
}

// MARK: - Currency Text Field

/// A text field that binds to a Double and accepts only numeric input.
private struct CurrencyTextField: View {
    @Binding var value: Double
    let placeholder: String

    @State private var text: String = ""
    @FocusState private var isFocused: Bool

    var body: some View {
        HStack(spacing: 8) {
            Text("$")
                .font(.body)
                .foregroundColor(.secondary)
            TextField(placeholder, text: $text)
                .keyboardType(.numberPad)
                .focused($isFocused)
                .onChange(of: text) { _, newValue in
                    let filtered = newValue.filter { $0.isNumber }
                    if filtered != newValue { text = filtered }
                    if let parsed = Double(filtered) {
                        value = parsed
                    } else if filtered.isEmpty {
                        value = 0
                    }
                }
        }
        .padding()
        .background(Color(.systemBackground))
        .cornerRadius(10)
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .stroke(isFocused ? Color.driveBaiPrimary : Color.gray.opacity(0.3), lineWidth: 1)
        )
        .onAppear {
            text = value > 0 ? String(Int(value)) : ""
        }
    }
}

// MARK: - Preview

#Preview {
    CreateListingFlowView()
}
