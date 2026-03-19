import Foundation
import Combine

// MARK: - Auth State

enum AuthState: Equatable {
    case unknown
    case unauthenticated
    case authenticated(UserProfile)

    var isAuthenticated: Bool {
        if case .authenticated = self {
            return true
        }
        return false
    }

    var user: UserProfile? {
        if case .authenticated(let user) = self {
            return user
        }
        return nil
    }
}

// MARK: - Auth Store

@MainActor
final class AuthStore: ObservableObject {
    static let shared = AuthStore()

    @Published private(set) var state: AuthState = .unknown
    @Published private(set) var isLoading = false
    @Published var error: String?

    private let apiClient: APIClientProtocol
    private let keychain: KeychainService

    // Registration state (for multi-step flow)
    @Published var pendingEmail: String?
    @Published var pendingRole: UserRole?

    // Document state (for onboarding)
    @Published private(set) var documents: [Document] = []

    init(
        apiClient: APIClientProtocol = APIClient.shared,
        keychain: KeychainService = .shared
    ) {
        self.apiClient = apiClient
        self.keychain = keychain
    }

    // MARK: - Initialization

    func checkAuthState() async {
        guard keychain.getAccessToken() != nil else {
            state = .unauthenticated
            return
        }

        do {
            let user = try await apiClient.getCurrentUser()
            state = .authenticated(user)
        } catch {
            // Token is invalid or expired
            keychain.clearTokens()
            state = .unauthenticated
        }
    }

    // MARK: - Registration

    func register(
        email: String,
        password: String,
        firstName: String,
        lastName: String,
        phone: String?,
        role: UserRole
    ) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        let request = RegisterRequest(
            email: email,
            password: password,
            firstName: firstName,
            lastName: lastName,
            phone: phone,
            role: role
        )

        do {
            // Registration now returns tokens directly - user is logged in immediately
            let tokens = try await apiClient.register(request: request)

            try keychain.saveTokens(
                accessToken: tokens.accessToken,
                refreshToken: tokens.refreshToken,
                expiresAt: tokens.expiresAt
            )

            state = .authenticated(tokens.user)
            pendingEmail = nil
            pendingRole = nil
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    // MARK: - Email Verification (deprecated - kept for backwards compatibility)

    func verifyEmail(email: String, code: String) async throws {
        // Email verification is no longer required
        // Users are verified immediately upon registration
    }

    func resendVerificationCode(email: String) async throws {
        // Email verification is no longer required
    }

    // MARK: - Login

    func login(email: String, password: String) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        let request = LoginRequest(email: email, password: password)

        do {
            let tokens = try await apiClient.login(request: request)

            try keychain.saveTokens(
                accessToken: tokens.accessToken,
                refreshToken: tokens.refreshToken,
                expiresAt: tokens.expiresAt
            )

            state = .authenticated(tokens.user)
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    // MARK: - Password Reset

    func forgotPassword(email: String) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        let request = ForgotPasswordRequest(email: email)

        do {
            _ = try await apiClient.forgotPassword(request: request)
            pendingEmail = email
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    func resetPassword(token: String, newPassword: String) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        let request = ResetPasswordRequest(
            token: token,
            newPassword: newPassword
        )

        do {
            _ = try await apiClient.resetPassword(request: request)
            pendingEmail = nil
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    // MARK: - Logout

    func logout() async {
        isLoading = true
        defer { isLoading = false }

        if let refreshToken = keychain.getRefreshToken() {
            let request = RefreshTokenRequest(refreshToken: refreshToken)
            _ = try? await apiClient.logout(request: request)
        }

        keychain.clearTokens()
        state = .unauthenticated
        pendingEmail = nil
        pendingRole = nil
        pendingRegistrationTokenData = nil
        error = nil
        documents = []

        // Clear all user-specific stores to prevent data leaking between users
        OwnerCarsStore.shared.clearAll()
        DiscoverViewModel.shared.clearAll()
        LikedListingsStore.shared.clearAll()
        WebSocketManager.shared.disconnect()
        ChatsListViewModel.shared.clearAll()
    }

    // MARK: - Profile Update

    func setRole(_ role: UserRole) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        let request = UpdateProfileRequest(
            role: role,
            firstName: nil,
            lastName: nil,
            phone: nil
        )

        do {
            let response = try await apiClient.updateProfile(request: request)
            state = .authenticated(response.data)
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    func refreshCurrentUser() async {
        guard state.isAuthenticated else { return }

        do {
            let user = try await apiClient.getCurrentUser()
            state = .authenticated(user)
        } catch {
            // Silently fail - user is still authenticated with old data
        }
    }

    // MARK: - Documents

    func fetchDocuments() async {
        guard state.isAuthenticated else { return }

        do {
            documents = try await apiClient.fetchDocuments()
        } catch {
            // Silently fail - documents will remain empty
            #if DEBUG
            print("[AuthStore] Failed to fetch documents: \(error)")
            #endif
        }
    }

    func uploadDocument(type: DocumentType, fileData: Data, filename: String, mimeType: String) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        do {
            let document = try await apiClient.uploadDocument(
                type: type,
                fileData: fileData,
                filename: filename,
                mimeType: mimeType
            )

            // Update local documents array
            documents.removeAll { $0.type == type }
            documents.append(document)

            // Refresh user to get updated onboarding status
            await refreshCurrentUser()
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    func deleteDocument(id: UUID) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        do {
            _ = try await apiClient.deleteDocument(id: id)

            // Remove from local array
            documents.removeAll { $0.id == id }
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    // MARK: - Profile Photo

    func uploadProfilePhoto(fileData: Data, filename: String, mimeType: String) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        do {
            let updatedUser = try await apiClient.uploadProfilePhoto(
                fileData: fileData,
                filename: filename,
                mimeType: mimeType
            )

            // Update state with new user profile
            state = .authenticated(updatedUser)
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    // MARK: - Onboarding

    func completeOnboarding() async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        do {
            _ = try await apiClient.completeOnboarding()

            // Refresh user to get updated onboarding status
            await refreshCurrentUser()
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    /// Returns true if the user needs to complete onboarding
    var needsOnboarding: Bool {
        guard let user = state.user else { return false }
        return user.needsOnboarding
    }

    /// Returns true if the driver needs to upload documents
    var needsDocumentUpload: Bool {
        guard let user = state.user else { return false }
        return user.role == .driver && user.needsOnboarding
    }

    /// Check if driver has all required documents uploaded
    func hasRequiredDocuments() -> Bool {
        let hasLicense = documents.contains { $0.type == .driversLicense }
        let hasRegistration = documents.contains { $0.type == .registration }
        return hasLicense && hasRegistration
    }

    // MARK: - Helpers

    func clearError() {
        error = nil
    }

    func clearPendingState() {
        pendingEmail = nil
        pendingRole = nil
    }

    // MARK: - OTP Passwordless Login

    /// Holds the registration token returned when OTP verify finds no existing user.
    /// The OTPLoginView passes this to SignupFlowView to complete registration.
    @Published var pendingRegistrationTokenData: RegistrationTokenData?

    /// Step 1 — request a 6-digit OTP for the given email.
    func requestLoginOTP(email: String) async throws {
        isLoading = true
        error = nil
        defer { isLoading = false }

        do {
            _ = try await apiClient.requestLoginOTP(email: email)
            pendingEmail = email
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    /// Step 2 — submit the OTP code.
    /// Returns `true` when the result is a login (tokens saved, state set to authenticated).
    /// Returns `false` when the result is a registration prompt (pendingRegistrationTokenData set).
    @discardableResult
    func verifyLoginOTP(email: String, code: String) async throws -> Bool {
        isLoading = true
        error = nil
        defer { isLoading = false }

        do {
            let response = try await apiClient.verifyLoginOTP(email: email, code: code)

            if let tokens = response.asAuthTokens {
                // Existing user — save tokens and transition to authenticated
                try keychain.saveTokens(
                    accessToken: tokens.accessToken,
                    refreshToken: tokens.refreshToken,
                    expiresAt: tokens.expiresAt
                )
                state = .authenticated(tokens.user)
                pendingEmail = nil
                return true
            }

            if let regData = response.asRegistrationData {
                // New user — store registration token data for the signup flow
                pendingRegistrationTokenData = regData
                pendingEmail = regData.email
                return false
            }

            throw APIError.unknown
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }

    /// Step 3 (new user path) — complete registration using the registration token.
    func completeOTPRegistration(
        firstName: String,
        lastName: String,
        password: String,
        role: UserRole,
        phone: String?
    ) async throws {
        guard let regData = pendingRegistrationTokenData else {
            throw APIError.serverError(code: "REGISTRATION_TOKEN_REQUIRED", message: "No registration token found. Please restart the OTP flow.")
        }

        isLoading = true
        error = nil
        defer { isLoading = false }

        let request = CompleteRegistrationRequest(
            registrationToken: regData.registrationToken,
            firstName: firstName,
            lastName: lastName,
            password: password,
            phone: phone,
            role: role
        )

        do {
            let tokens = try await apiClient.completeRegistrationWithToken(request)
            try keychain.saveTokens(
                accessToken: tokens.accessToken,
                refreshToken: tokens.refreshToken,
                expiresAt: tokens.expiresAt
            )
            state = .authenticated(tokens.user)
            pendingEmail = nil
            pendingRegistrationTokenData = nil
        } catch let apiError as APIError {
            error = apiError.errorDescription
            throw apiError
        }
    }
}
