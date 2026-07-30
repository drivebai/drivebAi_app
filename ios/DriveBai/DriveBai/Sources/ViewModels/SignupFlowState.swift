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

// MARK: - Signup Mode

enum SignupMode: Equatable {
    /// Standard email/password registration
    case normal
    /// OTP-verified registration — email is pre-filled and locked, uses /auth/otp/complete-registration
    case otp(registrationToken: String, email: String)
}

// MARK: - Email Availability

/// State machine for the inline "is this email already in use?" check on
/// Step 1 of signup. Lives on SignupFlowState; driven by a debounced Task
/// that calls APIClient.checkEmail. Continue is gated on `!= .taken` and
/// `!= .checking`; on `.networkError` the flow falls through to the
/// backend's 409 EMAIL_TAKEN at register-time.
enum EmailAvailability: Equatable {
    case idle
    case checking
    case available
    case taken
    case networkError
}

// MARK: - Phone country (A3)

/// A dial-code entry for the signup phone field. `minDigits...maxDigits`
/// bounds the NATIONAL number (after the dial code) so the field can cap
/// input per country; the assembled value is E.164 (dialCode + digits),
/// which is exactly what the backend validates and stores.
struct PhoneCountry: Identifiable, Equatable {
    let id: String        // ISO-ish key
    let flag: String
    let name: String
    let dialCode: String  // includes "+"
    let minDigits: Int
    let maxDigits: Int

    static let `default` = all[0]

    static let all: [PhoneCountry] = [
        PhoneCountry(id: "US", flag: "🇺🇸", name: "United States", dialCode: "+1", minDigits: 10, maxDigits: 10),
        PhoneCountry(id: "CA", flag: "🇨🇦", name: "Canada", dialCode: "+1", minDigits: 10, maxDigits: 10),
        PhoneCountry(id: "KZ", flag: "🇰🇿", name: "Kazakhstan", dialCode: "+7", minDigits: 10, maxDigits: 10),
        PhoneCountry(id: "GB", flag: "🇬🇧", name: "United Kingdom", dialCode: "+44", minDigits: 9, maxDigits: 10),
        PhoneCountry(id: "DE", flag: "🇩🇪", name: "Germany", dialCode: "+49", minDigits: 9, maxDigits: 11),
        PhoneCountry(id: "FR", flag: "🇫🇷", name: "France", dialCode: "+33", minDigits: 9, maxDigits: 9),
        PhoneCountry(id: "ES", flag: "🇪🇸", name: "Spain", dialCode: "+34", minDigits: 9, maxDigits: 9),
        PhoneCountry(id: "IT", flag: "🇮🇹", name: "Italy", dialCode: "+39", minDigits: 9, maxDigits: 10),
        PhoneCountry(id: "NL", flag: "🇳🇱", name: "Netherlands", dialCode: "+31", minDigits: 9, maxDigits: 9),
        PhoneCountry(id: "PL", flag: "🇵🇱", name: "Poland", dialCode: "+48", minDigits: 9, maxDigits: 9),
        PhoneCountry(id: "UA", flag: "🇺🇦", name: "Ukraine", dialCode: "+380", minDigits: 9, maxDigits: 9),
        PhoneCountry(id: "TR", flag: "🇹🇷", name: "Türkiye", dialCode: "+90", minDigits: 10, maxDigits: 10),
        PhoneCountry(id: "AE", flag: "🇦🇪", name: "UAE", dialCode: "+971", minDigits: 8, maxDigits: 9),
        PhoneCountry(id: "IN", flag: "🇮🇳", name: "India", dialCode: "+91", minDigits: 10, maxDigits: 10),
        PhoneCountry(id: "CN", flag: "🇨🇳", name: "China", dialCode: "+86", minDigits: 11, maxDigits: 11),
        PhoneCountry(id: "JP", flag: "🇯🇵", name: "Japan", dialCode: "+81", minDigits: 9, maxDigits: 10),
        PhoneCountry(id: "AU", flag: "🇦🇺", name: "Australia", dialCode: "+61", minDigits: 9, maxDigits: 9),
        PhoneCountry(id: "BR", flag: "🇧🇷", name: "Brazil", dialCode: "+55", minDigits: 10, maxDigits: 11),
        PhoneCountry(id: "MX", flag: "🇲🇽", name: "Mexico", dialCode: "+52", minDigits: 10, maxDigits: 10),
        PhoneCountry(id: "OTHER", flag: "🌐", name: "Other", dialCode: "+", minDigits: 6, maxDigits: 14),
    ]
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

    // Signup mode — controls which backend endpoint is called and whether email is editable
    var mode: SignupMode = .normal

    // Role Selection (Step 2)
    @Published var selectedRole: UserRole?

    // Profile Photo (Step 3)
    @Published var profilePhotoData: Data?
    @Published var profilePhotoImage: UIImage?

    // Email availability (Step 1, normal mode only)
    @Published var emailAvailability: EmailAvailability = .idle
    /// Debounced check task. Held so a follow-up edit can cancel the prior
    /// in-flight check before a stale result arrives. Also cancelled when
    /// the user goes back / dismisses the wizard.
    private var emailCheckTask: Task<Void, Never>?
    /// Last email value we kicked a check for. Lets us skip duplicate work
    /// when SwiftUI fires .onChange for no-op edits.
    private var lastCheckedEmail: String = ""

    // Phone entry (A3): country + national digits; `phone` (above) is kept
    // as the assembled E.164 value so the register call sites are untouched.
    @Published var phoneCountry: PhoneCountry = .default {
        didSet { recomputePhone() }
    }
    @Published var phoneNational: String = "" {
        didSet { recomputePhone() }
    }
    /// Same state machine as the email check — reused type, phone twin.
    @Published var phoneAvailability: EmailAvailability = .idle
    private var phoneCheckTask: Task<Void, Never>?

    private func recomputePhone() {
        let digits = phoneNational.filter(\.isNumber)
        if phoneCountry.id == "OTHER" {
            // "Other": the user types the dial code themselves in the field.
            phone = digits.isEmpty ? "" : "+" + digits
        } else {
            phone = digits.isEmpty ? "" : phoneCountry.dialCode + digits
        }
    }

    /// Phone is OPTIONAL at signup; when present it must be a complete
    /// national number for the chosen country.
    var isValidPhone: Bool {
        let digits = phoneNational.filter(\.isNumber)
        if digits.isEmpty { return true }
        return digits.count >= phoneCountry.minDigits && digits.count <= phoneCountry.maxDigits
    }
    var isPhoneComplete: Bool {
        let digits = phoneNational.filter(\.isNumber)
        return !digits.isEmpty && isValidPhone
    }

    // General State
    @Published var isLoading: Bool = false
    @Published var error: String?

    // Track if user is registered (authenticated)
    @Published var isRegistered: Bool = false

    // Track navigation direction for smooth transitions
    @Published var isNavigatingForward: Bool = true

    // MARK: - Init

    init(mode: SignupMode = .normal) {
        self.mode = mode
        if case .otp(_, let prefilledEmail) = mode {
            self.email = prefilledEmail
        }
    }

    // MARK: - Computed Properties

    var isUserInfoValid: Bool {
        let emailOK: Bool
        if case .otp = mode {
            emailOK = true // already verified via OTP
        } else {
            // Format must be valid AND the availability check must not be
            // mid-flight or known-taken. `.idle`, `.available`, and
            // `.networkError` all pass (network error falls through to the
            // register-time 409 EMAIL_TAKEN backstop).
            guard isValidEmail else { return false }
            switch emailAvailability {
            case .taken, .checking:
                return false
            case .idle, .available, .networkError:
                break
            }
            emailOK = true
        }
        // Phone is optional, but when present it must be complete and not
        // known-taken — the same timing contract as the email field.
        let phoneOK: Bool
        if phoneNational.filter(\.isNumber).isEmpty {
            phoneOK = true
        } else {
            guard isValidPhone else { return false }
            switch phoneAvailability {
            case .taken, .checking:
                return false
            case .idle, .available, .networkError:
                break
            }
            phoneOK = true
        }
        return !firstName.isEmpty &&
        !lastName.isEmpty &&
        emailOK &&
        phoneOK &&
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

    // MARK: - Email Availability Check

    /// Schedule an availability check for the current email value with a
    /// debounce. Idempotent: caller can call this on every keystroke;
    /// the prior task is cancelled if it hasn't completed yet. No-ops in
    /// OTP mode (email is pre-verified) and when the format is invalid
    /// (we never spam the backend with junk).
    ///
    /// - Parameters:
    ///   - debounceMillis: how long to wait after the last keystroke
    ///     before hitting the network. 600ms is gentle enough that a
    ///     fast typist generates exactly one request per email change.
    ///   - apiClient: injected so tests can sub in a fake; defaults to
    ///     the shared client used everywhere else in signup.
    func scheduleEmailAvailabilityCheck(
        debounceMillis: Int = 600,
        apiClient: APIClientProtocol = APIClient.shared
    ) {
        // OTP path: email is already verified; never bother the backend.
        if case .otp = mode { return }

        emailCheckTask?.cancel()

        let normalized = email.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !normalized.isEmpty else {
            emailAvailability = .idle
            return
        }
        guard isValidEmail else {
            // Bad format already shown by the existing validation row; clear
            // any stale availability so the user isn't shown a contradiction.
            emailAvailability = .idle
            return
        }

        emailAvailability = .checking
        emailCheckTask = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(debounceMillis))
            guard let self else { return }
            if Task.isCancelled { return }
            // Bail if the user kept typing past the debounce — there's
            // already a fresh task scheduled with the newer value.
            guard normalized == self.email.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() else { return }

            do {
                let available = try await apiClient.checkEmail(normalized)
                if Task.isCancelled { return }
                self.lastCheckedEmail = normalized
                self.emailAvailability = available ? .available : .taken
            } catch is CancellationError {
                // Superseded by a later check; nothing to do.
            } catch {
                if Task.isCancelled { return }
                self.emailAvailability = .networkError
            }
        }
    }

    /// Phone twin of scheduleEmailAvailabilityCheck: same 600ms debounce,
    /// same cancel-stale semantics, same fall-through-to-409 on network
    /// errors (register's PHONE_TAKEN backstop).
    func schedulePhoneAvailabilityCheck(
        debounceMillis: Int = 600,
        apiClient: APIClientProtocol = APIClient.shared
    ) {
        phoneCheckTask?.cancel()

        guard isPhoneComplete else {
            // Empty or incomplete number: nothing to ask the server yet.
            phoneAvailability = .idle
            return
        }
        let candidate = phone

        phoneAvailability = .checking
        phoneCheckTask = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(debounceMillis))
            guard let self else { return }
            if Task.isCancelled { return }
            guard candidate == self.phone else { return }

            do {
                let available = try await apiClient.checkPhone(candidate)
                if Task.isCancelled { return }
                self.phoneAvailability = available ? .available : .taken
            } catch is CancellationError {
                // Superseded by a later check; nothing to do.
            } catch {
                if Task.isCancelled { return }
                self.phoneAvailability = .networkError
            }
        }
    }

    /// Cancel any pending availability check — used on back navigation /
    /// wizard dismiss so a slow network response can't repaint state for
    /// a screen the user already left.
    func cancelEmailAvailabilityCheck() {
        emailCheckTask?.cancel()
        emailCheckTask = nil
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
        cancelEmailAvailabilityCheck()
        currentStep = .userInfo
        firstName = ""
        lastName = ""
        email = ""
        phone = ""
        password = ""
        confirmPassword = ""
        acceptedTerms = false
        emailAvailability = .idle
        lastCheckedEmail = ""
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
