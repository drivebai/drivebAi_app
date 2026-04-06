import SwiftUI
import PhotosUI
import CoreLocation

// MARK: - Create Listing Flow State

enum CreateListingStep: Int, CaseIterable {
    case basicInfo
    case details
    case pricing
    case requirements
    case photos
    case review

    var title: String {
        switch self {
        case .basicInfo: return "Basic Info"
        case .details: return "Car Details"
        case .pricing: return "Pricing"
        case .requirements: return "Requirements"
        case .photos: return "Photos"
        case .review: return "Review"
        }
    }
}

@MainActor
class CreateListingState: ObservableObject {
    // Basic Info
    @Published var make: String = ""
    @Published var model: String = ""
    @Published var year: Int = Calendar.current.component(.year, from: Date())

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

    // Navigation
    @Published var currentStep: CreateListingStep = .basicInfo
    @Published var isNavigatingForward: Bool = true
    @Published var isLoading: Bool = false
    @Published var error: String?

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
        (!isForRent || weeklyRentPrice >= 50) &&
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

    // Create Car
    func createCar(ownerId: UUID, ownerName: String) -> Car {
        let specs = CarSpecs(
            bodyType: bodyType,
            fuelType: fuelType,
            mileage: mileage,
            year: year,
            make: make,
            model: model
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
        dismiss()
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
            title: "Set Your Pricing",
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
                        Text("Weekly Rent Price")
                            .font(.subheadline)
                            .foregroundColor(.secondary)

                        CurrencyTextField(value: $state.weeklyRentPrice, placeholder: "350")

                        Text("Minimum $50")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                    .padding()
                    .background(Color(.systemGray6))
                    .cornerRadius(12)
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
                        Text("Sale Price")
                            .font(.subheadline)
                            .foregroundColor(.secondary)

                        CurrencyTextField(value: $state.salePrice, placeholder: "25000")

                        Text("Minimum $1,000")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                    .padding()
                    .background(Color(.systemGray6))
                    .cornerRadius(12)
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
            subtitle: "Set the requirements for drivers",
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
                    Text("Required Insurance Coverage")
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
                    // Remote image from server
                    AsyncImage(url: fullURL) { phase in
                        switch phase {
                        case .empty:
                            loadingPlaceholder
                        case .success(let image):
                            image
                                .resizable()
                                .scaledToFill()
                                .frame(width: geometry.size.width, height: geometry.size.height)
                                .clipped()
                        case .failure:
                            emptyPlaceholder
                        @unknown default:
                            emptyPlaceholder
                        }
                    }
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

// MARK: - Step 6: Review

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
