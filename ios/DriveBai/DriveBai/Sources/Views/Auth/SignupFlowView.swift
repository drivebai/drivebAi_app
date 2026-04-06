import SwiftUI

/// Coordinator view that manages the multi-step signup wizard flow
/// Single entry point for the entire signup process
struct SignupFlowView: View {
    @EnvironmentObject private var authStore: AuthStore
    @StateObject private var signupFlow: SignupFlowState

    @Environment(\.dismiss) private var dismiss

    init(mode: SignupMode = .normal) {
        _signupFlow = StateObject(wrappedValue: SignupFlowState(mode: mode))
    }

    /// Direction-aware transition for step navigation
    private var stepTransition: AnyTransition {
        .asymmetric(
            insertion: .move(edge: signupFlow.isNavigatingForward ? .trailing : .leading),
            removal: .move(edge: signupFlow.isNavigatingForward ? .leading : .trailing)
        )
    }

    var body: some View {
        NavigationStack {
            ZStack {
                // Step views with smooth bidirectional transitions
                Group {
                    switch signupFlow.currentStep {
                    case .userInfo:
                        SignupUserInfoStepView()
                            .transition(stepTransition)

                    case .role:
                        SignupRoleStepView(onRegister: handleRegister)
                            .transition(stepTransition)

                    case .profilePhoto:
                        SignupProfilePhotoStepView(
                            onContinue: handleProfilePhotoContinue,
                            onSkip: handleProfilePhotoSkip
                        )
                        .transition(stepTransition)

                    case .documents:
                        SignupDocumentsStepView(onComplete: handleOnboardingComplete)
                            .transition(stepTransition)
                    }
                }
                .animation(.easeInOut(duration: 0.3), value: signupFlow.currentStep)
            }
        }
        .environmentObject(signupFlow)
        .interactiveDismissDisabled(signupFlow.isRegistered || signupFlow.isLoading)
    }

    // MARK: - Flow Handlers

    /// Handle registration after role selection
    private func handleRegister() async throws {
        guard let role = signupFlow.selectedRole else { return }

        // If already registered (user went back), just advance to next step
        if signupFlow.isRegistered {
            signupFlow.goToNextStep()
            return
        }

        signupFlow.isLoading = true
        signupFlow.clearError()

        defer { signupFlow.isLoading = false }

        switch signupFlow.mode {
        case .normal:
            try await authStore.register(
                email: signupFlow.email,
                password: signupFlow.password,
                firstName: signupFlow.firstName,
                lastName: signupFlow.lastName,
                phone: signupFlow.phone.isEmpty ? nil : signupFlow.phone,
                role: role
            )
        case .otp:
            // Email is already verified; use the OTP registration token stored in AuthStore
            try await authStore.completeOTPRegistration(
                firstName: signupFlow.firstName,
                lastName: signupFlow.lastName,
                password: signupFlow.password,
                role: role,
                phone: signupFlow.phone.isEmpty ? nil : signupFlow.phone
            )
        }

        // Mark as registered and proceed to profile photo
        signupFlow.isRegistered = true
        signupFlow.goToNextStep()
    }

    /// Handle profile photo upload completion or continue
    private func handleProfilePhotoContinue() {
        if signupFlow.selectedRole == .driver {
            // Driver goes to documents step
            signupFlow.goToNextStep()
        } else {
            // Owner completes onboarding
            completeOnboarding()
        }
    }

    /// Handle skipping profile photo
    private func handleProfilePhotoSkip() {
        // Same as continue - profile photo is optional
        handleProfilePhotoContinue()
    }

    /// Complete onboarding for owners
    private func completeOnboarding() {
        signupFlow.isLoading = true
        signupFlow.clearError()

        Task { @MainActor in
            defer { signupFlow.isLoading = false }
            do {
                try await authStore.completeOnboarding()
                // ContentView will automatically switch to main tabs
            } catch {
                signupFlow.error = authStore.error
            }
        }
    }

    /// Handle driver onboarding completion
    private func handleOnboardingComplete() {
        // Driver has completed document upload and onboarding
        // ContentView will automatically switch to main tabs
    }
}

// MARK: - Step 1: User Info

struct SignupUserInfoStepView: View {
    @EnvironmentObject private var signupFlow: SignupFlowState
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        SignupStepContainer(
            title: signupFlow.mode == .normal ? "Create your account" : "Finish signing up",
            currentStep: signupFlow.currentStepIndex,
            totalSteps: signupFlow.totalSteps,
            showBackButton: true,
            isLoading: signupFlow.isLoading,
            canContinue: signupFlow.isUserInfoValid,
            onBack: { dismiss() },
            onContinue: { signupFlow.goToNextStep() }
        ) {
            VStack(spacing: 20) {
                // Legal Name Section
                VStack(alignment: .leading, spacing: 4) {
                    Text("Legal name")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    TextField("First name on ID", text: $signupFlow.firstName)
                        .textFieldStyle(DriveBaiTextFieldStyle())
                        .textContentType(.givenName)

                    TextField("Last name on ID", text: $signupFlow.lastName)
                        .textFieldStyle(DriveBaiTextFieldStyle())
                        .textContentType(.familyName)

                    Text("Make sure this matches the name on your government ID.")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                // Email Section
                VStack(alignment: .leading, spacing: 4) {
                    Text("Email")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    if case .otp = signupFlow.mode {
                        // Email pre-verified via OTP — show locked
                        HStack {
                            Text(signupFlow.email)
                                .foregroundColor(.primary)
                            Spacer()
                            Image(systemName: "checkmark.circle.fill")
                                .foregroundColor(.green)
                        }
                        .padding()
                        .background(Color(.systemGray6))
                        .cornerRadius(10)
                        Text("Verified via email code")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    } else {
                        TextField("Email", text: $signupFlow.email)
                            .textFieldStyle(DriveBaiTextFieldStyle())
                            .textContentType(.emailAddress)
                            .autocapitalization(.none)
                            .keyboardType(.emailAddress)

                        if !signupFlow.email.isEmpty && !signupFlow.isValidEmail {
                            Text("This email is not valid. Please check the spelling.")
                                .font(.caption)
                                .foregroundColor(.red)
                        } else if signupFlow.isValidEmail && !signupFlow.email.isEmpty {
                            HStack {
                                Image(systemName: "checkmark.circle.fill")
                                    .foregroundColor(.green)
                                Text("The email is valid")
                            }
                            .font(.caption)
                            .foregroundColor(.green)
                        }
                    }
                }

                // Phone Section
                VStack(alignment: .leading, spacing: 4) {
                    Text("Phone number")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    TextField("+1 234 567 8901", text: $signupFlow.phone)
                        .textFieldStyle(DriveBaiTextFieldStyle())
                        .textContentType(.telephoneNumber)
                        .keyboardType(.phonePad)

                    Text("We'll text you trip updates and receipts.")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                // Password Section
                VStack(alignment: .leading, spacing: 4) {
                    Text("Password")
                        .font(.subheadline)
                        .foregroundColor(.secondary)

                    if case .otp = signupFlow.mode {
                        Text("Set a password so you can also sign in without a code.")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }

                    SecureField("Password", text: $signupFlow.password)
                        .textFieldStyle(DriveBaiTextFieldStyle())
                        .textContentType(.newPassword)

                    SecureField("Confirm password", text: $signupFlow.confirmPassword)
                        .textFieldStyle(DriveBaiTextFieldStyle())
                        .textContentType(.newPassword)

                    if !signupFlow.confirmPassword.isEmpty && signupFlow.password != signupFlow.confirmPassword {
                        Text("Passwords do not match")
                            .font(.caption)
                            .foregroundColor(.red)
                    }

                    if !signupFlow.password.isEmpty && signupFlow.password.count < 8 {
                        Text("Password must be at least 8 characters")
                            .font(.caption)
                            .foregroundColor(.red)
                    }
                }

                // Terms
                HStack(alignment: .top, spacing: 12) {
                    Button(action: { signupFlow.acceptedTerms.toggle() }) {
                        Image(systemName: signupFlow.acceptedTerms ? "checkmark.square.fill" : "square")
                            .foregroundColor(signupFlow.acceptedTerms ? .driveBaiPrimary : .gray)
                    }

                    Text("I agree to [DriveBai Terms of Service](https://drivebai.com/terms), [Payments Terms of Service](https://drivebai.com/payments), and [Notification Policy](https://drivebai.com/privacy) and acknowledge the [Privacy Policy](https://drivebai.com/privacy).")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }

                // Error Message
                if let error = signupFlow.error {
                    SignupErrorBanner(message: error)
                }
            }
            .padding(.horizontal)
            .padding(.top, 16)
        }
    }
}

// MARK: - Step 2: Role Selection

struct SignupRoleStepView: View {
    @EnvironmentObject private var signupFlow: SignupFlowState
    @EnvironmentObject private var authStore: AuthStore

    let onRegister: () async throws -> Void

    var body: some View {
        SignupStepContainer(
            title: "Finish signing up",
            currentStep: signupFlow.currentStepIndex,
            totalSteps: signupFlow.totalSteps,
            showBackButton: true,
            isLoading: signupFlow.isLoading,
            canContinue: signupFlow.isRoleSelected,
            onBack: { signupFlow.goToPreviousStep() },
            onContinue: { handleContinue() }
        ) {
            VStack(spacing: 32) {
                // Header
                SignupStepHeader(
                    title: "Select your role",
                    subtitle: "Choose how you want to use DriveBai"
                )

                // Role Options
                VStack(spacing: 16) {
                    RoleSelectionCard(
                        role: .driver,
                        isSelected: signupFlow.selectedRole == .driver,
                        action: { signupFlow.selectedRole = .driver }
                    )

                    RoleSelectionCard(
                        role: .carOwner,
                        isSelected: signupFlow.selectedRole == .carOwner,
                        action: { signupFlow.selectedRole = .carOwner }
                    )
                }
                .padding(.horizontal)

                // Error Message
                if let error = signupFlow.error ?? authStore.error {
                    SignupErrorBanner(message: error)
                }
            }
        }
    }

    private func handleContinue() {
        Task {
            do {
                try await onRegister()
            } catch {
                signupFlow.error = authStore.error ?? error.localizedDescription
            }
        }
    }
}

// MARK: - Role Selection Card

struct RoleSelectionCard: View {
    let role: UserRole
    let isSelected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 16) {
                // Icon
                Image(systemName: role == .driver ? "car.fill" : "key.fill")
                    .font(.system(size: 32))
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 56, height: 56)
                    .background(Color.driveBaiPrimary.opacity(0.1))
                    .clipShape(Circle())

                // Text
                VStack(alignment: .leading, spacing: 4) {
                    Text(role == .driver ? "I need a car" : "I have a car")
                        .font(.headline)
                        .foregroundColor(.primary)

                    Text(role == .driver ? "I am a driver looking for a vehicle" : "I am a car owner looking for a driver")
                        .font(.caption)
                        .foregroundColor(.secondary)
                        .lineLimit(2)
                }

                Spacer()

                // Selection indicator
                Image(systemName: isSelected ? "checkmark.circle.fill" : "circle")
                    .foregroundColor(isSelected ? .driveBaiPrimary : .gray.opacity(0.5))
                    .font(.title2)
            }
            .padding()
            .background(Color(.systemBackground))
            .cornerRadius(12)
            .overlay(
                RoundedRectangle(cornerRadius: 12)
                    .stroke(isSelected ? Color.driveBaiPrimary : Color.gray.opacity(0.3), lineWidth: isSelected ? 2 : 1)
            )
        }
    }
}

// MARK: - Step 3: Profile Photo

struct SignupProfilePhotoStepView: View {
    @EnvironmentObject private var signupFlow: SignupFlowState
    @EnvironmentObject private var authStore: AuthStore

    let onContinue: () -> Void
    let onSkip: () -> Void

    @State private var selectedItem: PhotosPickerItem?
    @State private var isUploading = false
    @State private var isPickerPresented = false

    /// Determines the main button title based on state
    private var mainButtonTitle: String {
        signupFlow.hasProfilePhoto ? "Continue" : "Upload"
    }

    /// Determines the secondary button title based on state
    private var secondaryButtonTitle: String {
        signupFlow.hasProfilePhoto ? "Reupload" : "Skip"
    }

    var body: some View {
        SignupStepContainer(
            title: "Finish signing up",
            currentStep: signupFlow.currentStepIndex,
            totalSteps: signupFlow.totalSteps,
            showBackButton: true,
            isLoading: isUploading || signupFlow.isLoading,
            canContinue: true, // Always can continue
            continueTitle: mainButtonTitle,
            showSkip: true, // Always show secondary button
            skipTitle: secondaryButtonTitle,
            onBack: { signupFlow.goToPreviousStep() },
            onContinue: { handleMainButtonTap() },
            onSkip: { handleSecondaryButtonTap() }
        ) {
            VStack(spacing: 24) {
                // Header
                SignupStepHeader(
                    title: "Add a profile photo",
                    subtitle: "This helps other users recognize you and builds trust in our community."
                )

                // Avatar - tappable to open picker
                Button(action: { isPickerPresented = true }) {
                    ZStack {
                        if let image = signupFlow.profilePhotoImage {
                            Image(uiImage: image)
                                .resizable()
                                .scaledToFill()
                                .frame(width: 160, height: 160)
                                .clipShape(Circle())
                        } else {
                            Circle()
                                .fill(Color(.systemGray5))
                                .frame(width: 160, height: 160)
                                .overlay(
                                    VStack(spacing: 8) {
                                        Image(systemName: "camera.fill")
                                            .font(.system(size: 40))
                                            .foregroundColor(.gray.opacity(0.5))
                                        Text("Tap to add")
                                            .font(.caption)
                                            .foregroundColor(.gray.opacity(0.5))
                                    }
                                )
                        }

                        // Upload indicator
                        if isUploading {
                            Circle()
                                .fill(Color.black.opacity(0.5))
                                .frame(width: 160, height: 160)
                                .overlay(
                                    ProgressView()
                                        .tint(.white)
                                        .scaleEffect(1.5)
                                )
                        }
                    }
                }
                .disabled(isUploading)

                // Error Message
                if let error = signupFlow.error {
                    SignupErrorBanner(message: error)
                }
            }
            .padding(.top, 24)
        }
        .photosPicker(isPresented: $isPickerPresented, selection: $selectedItem, matching: .images)
        .onChange(of: selectedItem) { _, newItem in
            if let item = newItem {
                Task {
                    await loadImage(from: item)
                    selectedItem = nil
                }
            }
        }
    }

    // MARK: - Button Handlers

    /// Handles main button tap
    /// - No photo: Opens photo picker
    /// - Has photo: Uploads photo then advances
    private func handleMainButtonTap() {
        if signupFlow.hasProfilePhoto {
            // Has photo: Upload and continue
            uploadPhotoAndContinue()
        } else {
            // No photo: Open picker (NOT skip!)
            isPickerPresented = true
        }
    }

    /// Handles secondary button tap
    /// - No photo (Skip): Advances without uploading
    /// - Has photo (Reupload): Opens picker to replace image
    private func handleSecondaryButtonTap() {
        if signupFlow.hasProfilePhoto {
            // Reupload: Open picker to replace
            isPickerPresented = true
        } else {
            // Skip: Advance without photo
            onSkip()
        }
    }

    // MARK: - Image Loading

    @MainActor
    private func loadImage(from item: PhotosPickerItem) async {
        isUploading = true
        signupFlow.clearError()
        defer { isUploading = false }

        do {
            guard let data = try await item.loadTransferable(type: Data.self) else {
                signupFlow.error = "Failed to load selected image"
                return
            }

            guard let uiImage = UIImage(data: data) else {
                signupFlow.error = "Invalid image format"
                return
            }

            // Compress and resize if needed
            let resizedImage = resizeImage(uiImage, maxSize: 1024)
            guard let compressedData = resizedImage.jpegData(compressionQuality: 0.8) else {
                signupFlow.error = "Failed to process image"
                return
            }

            signupFlow.profilePhotoImage = resizedImage
            signupFlow.profilePhotoData = compressedData
        } catch {
            signupFlow.error = "Failed to load image: \(error.localizedDescription)"
        }
    }

    private func resizeImage(_ image: UIImage, maxSize: CGFloat) -> UIImage {
        let size = image.size

        guard size.width > maxSize || size.height > maxSize else {
            return image
        }

        let ratio = min(maxSize / size.width, maxSize / size.height)
        let newSize = CGSize(width: size.width * ratio, height: size.height * ratio)

        UIGraphicsBeginImageContextWithOptions(newSize, false, 1.0)
        image.draw(in: CGRect(origin: .zero, size: newSize))
        let newImage = UIGraphicsGetImageFromCurrentImageContext()
        UIGraphicsEndImageContext()

        return newImage ?? image
    }

    // MARK: - Upload

    private func uploadPhotoAndContinue() {
        guard let photoData = signupFlow.profilePhotoData else {
            onContinue()
            return
        }

        isUploading = true
        signupFlow.clearError()

        Task { @MainActor in
            defer { isUploading = false }
            do {
                try await authStore.uploadProfilePhoto(
                    fileData: photoData,
                    filename: "profile.jpg",
                    mimeType: "image/jpeg"
                )
                onContinue()
            } catch {
                signupFlow.error = "Failed to upload photo. Please try again."
            }
        }
    }
}

// MARK: - Step 4: Driver Documents

struct SignupDocumentsStepView: View {
    @EnvironmentObject private var signupFlow: SignupFlowState
    @EnvironmentObject private var authStore: AuthStore

    let onComplete: () -> Void

    @State private var isCompletingOnboarding = false

    var body: some View {
        SignupStepContainer(
            title: "Driver Verification",
            currentStep: signupFlow.currentStepIndex,
            totalSteps: signupFlow.totalSteps,
            showBackButton: true,
            isLoading: isCompletingOnboarding,
            canContinue: authStore.hasRequiredDocuments(),
            onBack: { signupFlow.goToPreviousStep() },
            onContinue: { completeOnboarding() }
        ) {
            VStack(spacing: 24) {
                // Header
                SignupStepHeader(
                    title: "Upload Your Documents",
                    subtitle: "We need to verify your identity before you can start driving.",
                    icon: "doc.badge.plus"
                )

                // Document cards will be embedded here
                // For now, show a placeholder that uses the existing DocumentUploadView logic
                EmbeddedDocumentUploadContent()

                // Info Text
                VStack(alignment: .leading, spacing: 8) {
                    Label("Accepted formats: JPEG, PNG", systemImage: "info.circle")
                    Label("Maximum file size: 10MB", systemImage: "doc")
                    Label("Documents will be reviewed within 24 hours", systemImage: "clock")
                }
                .font(.caption)
                .foregroundColor(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal)

                // Error Message
                if let error = signupFlow.error ?? authStore.error {
                    SignupErrorBanner(message: error)
                }
            }
        }
        .task {
            await authStore.fetchDocuments()
        }
    }

    private func completeOnboarding() {
        isCompletingOnboarding = true
        signupFlow.clearError()

        Task { @MainActor in
            defer { isCompletingOnboarding = false }
            do {
                try await authStore.completeOnboarding()
                onComplete()
            } catch {
                signupFlow.error = authStore.error ?? "Failed to complete onboarding"
            }
        }
    }
}

// MARK: - Embedded Document Upload Content

struct EmbeddedDocumentUploadContent: View {
    @EnvironmentObject private var authStore: AuthStore
    @EnvironmentObject private var signupFlow: SignupFlowState

    @State private var selectedLicenseItem: PhotosPickerItem?
    @State private var selectedRegistrationItem: PhotosPickerItem?
    @State private var isUploadingLicense = false
    @State private var isUploadingRegistration = false

    private var licenseDocument: Document? {
        authStore.documents.first { $0.type == .driversLicense }
    }

    private var registrationDocument: Document? {
        authStore.documents.first { $0.type == .registration }
    }

    var body: some View {
        VStack(spacing: 16) {
            DocumentUploadCard(
                type: .driversLicense,
                document: licenseDocument,
                isUploading: isUploadingLicense,
                selectedItem: $selectedLicenseItem,
                onDelete: { deleteLicense() }
            )
            .onChange(of: selectedLicenseItem) { _, newItem in
                if let item = newItem {
                    Task {
                        await uploadDocument(item: item, type: .driversLicense)
                        selectedLicenseItem = nil
                    }
                }
            }

            DocumentUploadCard(
                type: .registration,
                document: registrationDocument,
                isUploading: isUploadingRegistration,
                selectedItem: $selectedRegistrationItem,
                onDelete: { deleteRegistration() }
            )
            .onChange(of: selectedRegistrationItem) { _, newItem in
                if let item = newItem {
                    Task {
                        await uploadDocument(item: item, type: .registration)
                        selectedRegistrationItem = nil
                    }
                }
            }
        }
        .padding(.horizontal)
    }

    private func uploadDocument(item: PhotosPickerItem, type: DocumentType) async {
        if type == .driversLicense {
            isUploadingLicense = true
        } else {
            isUploadingRegistration = true
        }

        defer {
            if type == .driversLicense {
                isUploadingLicense = false
            } else {
                isUploadingRegistration = false
            }
        }

        signupFlow.clearError()

        do {
            guard let data = try await item.loadTransferable(type: Data.self) else {
                signupFlow.error = "Failed to load selected file"
                return
            }

            let mimeType: String
            let filename: String

            if let contentType = item.supportedContentTypes.first {
                if contentType.conforms(to: .png) {
                    mimeType = "image/png"
                    filename = "\(type.rawValue).png"
                } else {
                    mimeType = "image/jpeg"
                    filename = "\(type.rawValue).jpg"
                }
            } else {
                mimeType = "image/jpeg"
                filename = "\(type.rawValue).jpg"
            }

            try await authStore.uploadDocument(
                type: type,
                fileData: data,
                filename: filename,
                mimeType: mimeType
            )
        } catch {
            signupFlow.error = authStore.error ?? "Failed to upload document"
        }
    }

    private func deleteLicense() {
        guard let doc = licenseDocument else { return }
        Task {
            try? await authStore.deleteDocument(id: doc.id)
        }
    }

    private func deleteRegistration() {
        guard let doc = registrationDocument else { return }
        Task {
            try? await authStore.deleteDocument(id: doc.id)
        }
    }
}

import PhotosUI

#Preview {
    SignupFlowView()
        .environmentObject(AuthStore.shared)
}
