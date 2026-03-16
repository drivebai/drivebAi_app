import Foundation
import SwiftUI

// MARK: - Signup Step

enum SignupStep: Int, CaseIterable, Equatable {
    case userInfo = 0
    case role = 1
    case profilePhoto = 2
    case documents = 3  // Driver only

    var title: String {
        switch self {
        case .userInfo:
            return "Finish signing up"
        case .role:
            return "Finish signing up"
        case .profilePhoto:
            return "Finish signing up"
        case .documents:
            return "Driver Verification"
        }
    }

    /// Returns the steps for a given role
    /// Before role is selected, we show 3 steps (default)
    /// After role is selected:
    ///   - Owner: 3 steps
    ///   - Driver: 4 steps
    static func steps(for role: UserRole?) -> [SignupStep] {
        switch role {
        case .driver:
            return [.userInfo, .role, .profilePhoto, .documents]
        case .carOwner, .admin:
            return [.userInfo, .role, .profilePhoto]
        case nil:
            // Before role selection, show 3 steps (like owner)
            // This prevents jumping when role is selected for owners
            return [.userInfo, .role, .profilePhoto]
        }
    }

    /// Total step count for a given role
    static func totalSteps(for role: UserRole?) -> Int {
        steps(for: role).count
    }
}

// MARK: - Signup Flow State

@MainActor
final class SignupFlowState: ObservableObject {
    // MARK: - Published Properties

    @Published var currentStep: SignupStep = .userInfo

    // User Info (Step 1)
    @Published var firstName: String = ""
    @Published var lastName: String = ""
    @Published var email: String = ""
    @Published var phone: String = ""
    @Published var password: String = ""
    @Published var confirmPassword: String = ""
    @Published var acceptedTerms: Bool = false

    // Role Selection (Step 2)
    @Published var selectedRole: UserRole?

    // Profile Photo (Step 3)
    @Published var profilePhotoData: Data?
    @Published var profilePhotoImage: UIImage?

    // General State
    @Published var isLoading: Bool = false
    @Published var error: String?

    // Track if user is registered (authenticated)
    @Published var isRegistered: Bool = false

    // Track navigation direction for smooth transitions
    @Published var isNavigatingForward: Bool = true

    // MARK: - Computed Properties

    var isUserInfoValid: Bool {
        !firstName.isEmpty &&
        !lastName.isEmpty &&
        isValidEmail &&
        password.count >= 8 &&
        password == confirmPassword &&
        acceptedTerms
    }

    var isValidEmail: Bool {
        let emailRegex = #"^[A-Z0-9a-z._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$"#
        return email.range(of: emailRegex, options: .regularExpression) != nil
    }

    var isRoleSelected: Bool {
        selectedRole != nil
    }

    var hasProfilePhoto: Bool {
        profilePhotoData != nil
    }

    /// Active steps based on current role selection
    var activeSteps: [SignupStep] {
        SignupStep.steps(for: selectedRole)
    }

    /// Total number of steps for current role
    var totalSteps: Int {
        activeSteps.count
    }

    /// Current step index (0-based)
    var currentStepIndex: Int {
        activeSteps.firstIndex(of: currentStep) ?? 0
    }

    var isFirstStep: Bool {
        currentStep == .userInfo
    }

    var isLastStep: Bool {
        guard let lastStep = activeSteps.last else { return true }
        return currentStep == lastStep
    }

    /// Check if current step can proceed
    var canProceed: Bool {
        switch currentStep {
        case .userInfo:
            return isUserInfoValid
        case .role:
            return isRoleSelected
        case .profilePhoto:
            // Profile photo is optional but encouraged
            return true
        case .documents:
            // Documents validation handled by DocumentUploadView
            return true
        }
    }

    // MARK: - Navigation

    func goToNextStep() {
        guard let currentIndex = activeSteps.firstIndex(of: currentStep),
              currentIndex + 1 < activeSteps.count else { return }
        isNavigatingForward = true
        currentStep = activeSteps[currentIndex + 1]
    }

    func goToPreviousStep() {
        guard let currentIndex = activeSteps.firstIndex(of: currentStep),
              currentIndex > 0 else { return }
        isNavigatingForward = false
        currentStep = activeSteps[currentIndex - 1]
    }

    func goToStep(_ step: SignupStep) {
        guard activeSteps.contains(step) else { return }
        if let currentIndex = activeSteps.firstIndex(of: currentStep),
           let targetIndex = activeSteps.firstIndex(of: step) {
            isNavigatingForward = targetIndex > currentIndex
        }
        currentStep = step
    }

    // MARK: - Reset

    func reset() {
        currentStep = .userInfo
        firstName = ""
        lastName = ""
        email = ""
        phone = ""
        password = ""
        confirmPassword = ""
        acceptedTerms = false
        selectedRole = nil
        profilePhotoData = nil
        profilePhotoImage = nil
        isLoading = false
        error = nil
        isRegistered = false
        isNavigatingForward = true
    }

    func clearError() {
        error = nil
    }

    // MARK: - Resume from Onboarding Status

    /// Determine which step to show based on user's onboarding status
    func resumeFromStatus(_ status: OnboardingStatus, role: UserRole) {
        selectedRole = role
        isRegistered = true

        switch status {
        case .created, .roleSelected:
            // User registered but hasn't uploaded photo
            currentStep = .profilePhoto
        case .photoUploaded:
            // User uploaded photo
            if role == .driver {
                currentStep = .documents
            } else {
                // Owner is ready to complete - stay on profilePhoto
                currentStep = .profilePhoto
            }
        case .documentsUploaded:
            // Driver uploaded docs, ready to complete
            currentStep = .documents
        case .complete:
            // Already complete - this shouldn't happen in signup flow
            currentStep = .userInfo
        }
    }
}
