import Foundation

// MARK: - User Role

enum UserRole: String, Codable, CaseIterable {
    case driver = "driver"
    case carOwner = "car_owner"
    case admin = "admin"

    var displayName: String {
        switch self {
        case .driver:
            return "Driver"
        case .carOwner:
            return "Car Owner"
        case .admin:
            return "Admin"
        }
    }

    var description: String {
        switch self {
        case .driver:
            return "I'm a driver looking for a vehicle"
        case .carOwner:
            return "I'm a car owner looking for a driver"
        case .admin:
            return "Administrator"
        }
    }
}

// MARK: - Onboarding Status

enum OnboardingStatus: String, Codable {
    case created = "created"
    case roleSelected = "role_selected"
    case photoUploaded = "photo_uploaded"
    case documentsUploaded = "documents_uploaded"
    case complete = "complete"

    var isComplete: Bool {
        self == .complete
    }
}

// MARK: - User Profile

struct UserProfile: Codable, Equatable {
    let id: UUID
    let email: String
    let role: UserRole
    let firstName: String
    let lastName: String
    let phone: String?
    let isEmailVerified: Bool
    let onboardingStatus: OnboardingStatus
    let profilePhotoURL: String?

    enum CodingKeys: String, CodingKey {
        case id, email, role, phone
        case firstName = "first_name"
        case lastName = "last_name"
        case isEmailVerified = "is_email_verified"
        case onboardingStatus = "onboarding_status"
        case profilePhotoURL = "profile_photo_url"
    }

    var fullName: String {
        "\(firstName) \(lastName)"
    }

    var needsOnboarding: Bool {
        !onboardingStatus.isComplete
    }
}

// MARK: - Auth Tokens

struct AuthTokens: Codable {
    let accessToken: String
    let refreshToken: String
    let expiresAt: Date
    let user: UserProfile

    enum CodingKeys: String, CodingKey {
        case accessToken = "access_token"
        case refreshToken = "refresh_token"
        case expiresAt = "expires_at"
        case user
    }
}

// MARK: - API Requests

struct RegisterRequest: Codable {
    let email: String
    let password: String
    let firstName: String
    let lastName: String
    let phone: String?
    let role: UserRole

    enum CodingKeys: String, CodingKey {
        case email, password, phone, role
        case firstName = "first_name"
        case lastName = "last_name"
    }
}

// RegisterResponse now returns tokens directly (same as login)
typealias RegisterResponse = AuthTokens

struct VerifyEmailRequest: Codable {
    let email: String
    let code: String
}

struct LoginRequest: Codable {
    let email: String
    let password: String
}

struct RefreshTokenRequest: Codable {
    let refreshToken: String

    enum CodingKeys: String, CodingKey {
        case refreshToken = "refresh_token"
    }
}

struct ForgotPasswordRequest: Codable {
    let email: String
}

struct ResetPasswordRequest: Codable {
    let token: String
    let newPassword: String

    enum CodingKeys: String, CodingKey {
        case token
        case newPassword = "new_password"
    }
}

struct ResendOTPRequest: Codable {
    let email: String
    let purpose: String
}

// MARK: - OTP Login

struct OTPLoginRequestBody: Codable {
    let email: String
}

struct OTPVerifyRequestBody: Codable {
    let email: String
    let code: String
}

/// Decoded from POST /auth/otp/verify.
/// The `kind` field discriminates between the two cases:
///   "login"    → existing user, full tokens included
///   "register" → new user, registration_token + email included
struct OTPVerifyResponse: Codable {
    let kind: String

    // login fields
    let accessToken: String?
    let refreshToken: String?
    let expiresAt: Date?
    let user: UserProfile?

    // register fields
    let registrationToken: String?
    let email: String?

    enum CodingKeys: String, CodingKey {
        case kind
        case accessToken = "access_token"
        case refreshToken = "refresh_token"
        case expiresAt = "expires_at"
        case user
        case registrationToken = "registration_token"
        case email
    }

    var asAuthTokens: AuthTokens? {
        guard kind == "login",
              let at = accessToken,
              let rt = refreshToken,
              let exp = expiresAt,
              let u = user else { return nil }
        return AuthTokens(accessToken: at, refreshToken: rt, expiresAt: exp, user: u)
    }

    var asRegistrationData: RegistrationTokenData? {
        guard kind == "register",
              let tok = registrationToken,
              let em = email else { return nil }
        return RegistrationTokenData(registrationToken: tok, email: em)
    }
}

/// Carries the short-lived registration token + verified email from OTP verify.
struct RegistrationTokenData {
    let registrationToken: String
    let email: String
}

/// Request body for POST /auth/otp/complete-registration.
struct CompleteRegistrationRequest: Codable {
    let registrationToken: String
    let firstName: String
    let lastName: String
    let password: String
    let phone: String?
    let role: UserRole

    enum CodingKeys: String, CodingKey {
        case registrationToken = "registration_token"
        case firstName = "first_name"
        case lastName = "last_name"
        case password, phone, role
    }
}

struct UpdateProfileRequest: Codable {
    let role: UserRole?
    let firstName: String?
    let lastName: String?
    let phone: String?

    enum CodingKeys: String, CodingKey {
        case role, phone
        case firstName = "first_name"
        case lastName = "last_name"
    }
}

// MARK: - API Responses

struct MessageResponse: Codable {
    let message: String
}

struct UpdateProfileResponse: Codable {
    let message: String
    let data: UserProfile
}

struct LikedListingsResponse: Codable {
    let likedListingIds: [UUID]

    enum CodingKeys: String, CodingKey {
        case likedListingIds = "liked_listing_ids"
    }
}

// MARK: - Document

enum DocumentType: String, Codable, CaseIterable {
    case driversLicense = "drivers_license"
    case registration = "registration"
    case commercialLicense = "commercial_license"
    case tlcLicense = "tlc_license"
    case other = "other"

    var displayName: String {
        switch self {
        case .driversLicense:    return "Driver's License"
        case .registration:      return "Vehicle Registration"
        case .commercialLicense: return "Commercial License"
        case .tlcLicense:        return "TLC License"
        case .other:             return "Other Document"
        }
    }

    /// Optional supporting documents a driver can attach during onboarding.
    /// The required document — drivers_license — is handled separately by
    /// AuthStore.hasRequiredDocuments() so this list only enumerates the
    /// slots that should appear under the "Optional" header.
    static var optionalDriverDocs: [DocumentType] {
        [.registration, .commercialLicense, .tlcLicense, .other]
    }
}

enum DocumentStatus: String, Codable {
    case uploaded = "uploaded"
    case verified = "verified"
    case rejected = "rejected"

    var displayName: String {
        switch self {
        case .uploaded:
            return "Pending Review"
        case .verified:
            return "Verified"
        case .rejected:
            return "Rejected"
        }
    }
}

struct Document: Codable, Identifiable, Equatable {
    let id: UUID
    let type: DocumentType
    let fileName: String
    let fileSize: Int64
    let status: DocumentStatus
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id, type, status
        case fileName = "file_name"
        case fileSize = "file_size"
        case createdAt = "created_at"
    }
}

// MARK: - API Error

struct APIErrorResponse: Codable {
    let error: APIErrorDetail
}

struct APIErrorDetail: Codable {
    let code: String
    let message: String
    let details: APIErrorDetails?
}

/// Server-provided structured error context. Currently only DRIVER_DOCS_REQUIRED
/// uses this, to report which document types still need uploading.
struct APIErrorDetails: Codable {
    let missingTypes: [String]?

    enum CodingKeys: String, CodingKey {
        case missingTypes = "missing_types"
    }
}

enum APIErrorCode: String {
    case emailTaken = "EMAIL_TAKEN"
    case invalidCredentials = "INVALID_CREDENTIALS"
    case otpInvalid = "OTP_INVALID"
    case otpExpired = "OTP_EXPIRED"
    case emailNotVerified = "EMAIL_NOT_VERIFIED"
    case rateLimited = "RATE_LIMITED"
    case userNotFound = "USER_NOT_FOUND"
    case invalidRole = "INVALID_ROLE"
    case invalidInput = "INVALID_INPUT"
    case unauthorized = "UNAUTHORIZED"
    case tokenExpired = "TOKEN_EXPIRED"
    case tokenInvalid = "TOKEN_INVALID"
    case internalError = "INTERNAL_ERROR"
    case driverDocsRequired = "DRIVER_DOCS_REQUIRED"
    case profileNotFound = "PROFILE_NOT_FOUND"

    var userMessage: String {
        switch self {
        case .emailTaken:
            return "This email is already registered. Try logging in instead."
        case .invalidCredentials:
            return "Invalid email or password. Please try again."
        case .otpInvalid:
            return "Invalid verification code. Please check and try again."
        case .otpExpired:
            return "Verification code has expired. Please request a new one."
        case .emailNotVerified:
            return "Please verify your email first."
        case .rateLimited:
            return "Too many requests. Please wait a few minutes and try again."
        case .userNotFound:
            return "Account not found."
        case .invalidRole:
            return "Invalid role selected."
        case .invalidInput:
            return "Please check your input and try again."
        case .unauthorized:
            return "Please log in to continue."
        case .tokenExpired:
            return "Your session has expired. Please log in again."
        case .tokenInvalid:
            return "Invalid session. Please log in again."
        case .internalError:
            return "Something went wrong. Please try again later."
        case .driverDocsRequired:
            return "Driver documents are required before switching to Driver mode."
        case .profileNotFound:
            return "Profile not found."
        }
    }
}

// MARK: - Mode Profiles

/// One role-scoped sub-account under a user identity. Each user may have up to
/// one driver profile and one car_owner profile; one is active at any time and
/// determines which role-scoped tabs + permissions the app uses.
struct ProfileSummary: Codable, Equatable, Identifiable {
    let id: UUID
    let role: UserRole
    let onboardingStatus: OnboardingStatus
    let hasRequiredDocs: Bool
    let isActive: Bool
    let createdAt: String

    enum CodingKeys: String, CodingKey {
        case id, role
        case onboardingStatus = "onboarding_status"
        case hasRequiredDocs = "has_required_docs"
        case isActive = "is_active"
        case createdAt = "created_at"
    }
}

struct ListProfilesResponse: Codable {
    let profiles: [ProfileSummary]
    let activeProfileId: UUID?
    let activeRole: UserRole?

    enum CodingKeys: String, CodingKey {
        case profiles
        case activeProfileId = "active_profile_id"
        case activeRole = "active_role"
    }
}

struct CreateProfileRequest: Codable {
    let role: UserRole
}

/// Request body for POST /me/active-profile. `role` is the preferred field;
/// `profileId` is accepted too but the server treats them equivalently.
struct SwitchProfileRequest: Codable {
    let role: UserRole?
    let profileId: UUID?

    enum CodingKeys: String, CodingKey {
        case role
        case profileId = "profile_id"
    }
}

/// Successful response from POST /me/active-profile: a fresh token pair (the
/// role embedded in the JWT now matches the new active profile) + the updated
/// user + the activated profile summary.
struct SwitchProfileResponse: Codable {
    let accessToken: String
    let refreshToken: String
    let expiresAt: Date
    let user: UserProfile
    let activeProfile: ProfileSummary

    enum CodingKeys: String, CodingKey {
        case accessToken = "access_token"
        case refreshToken = "refresh_token"
        case expiresAt = "expires_at"
        case user
        case activeProfile = "active_profile"
    }
}
