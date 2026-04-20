import Foundation

// MARK: - ISO8601 Date Parsing

/// A tolerant ISO8601 date parser that handles various formats including:
/// - No fractional seconds: 2026-01-23T12:50:19Z
/// - Milliseconds (3 digits): 2026-01-23T12:50:19.266Z
/// - Microseconds (6 digits): 2026-01-23T12:50:19.266123Z
/// - Nanoseconds (7+ digits): 2026-01-23T12:50:19.2656725Z
enum ISO8601DateParser {
    private static let formatters: [DateFormatter] = {
        let formats = [
            "yyyy-MM-dd'T'HH:mm:ssZ",
            "yyyy-MM-dd'T'HH:mm:ss.SSSZ",
            "yyyy-MM-dd'T'HH:mm:ss.SSSSSSZ",
            "yyyy-MM-dd'T'HH:mm:ss.SSSSSSSZ",
            "yyyy-MM-dd'T'HH:mm:ssXXXXX",
            "yyyy-MM-dd'T'HH:mm:ss.SSSXXXXX",
            "yyyy-MM-dd'T'HH:mm:ss.SSSSSSXXXXX",
            "yyyy-MM-dd'T'HH:mm:ss.SSSSSSSXXXXX"
        ]
        return formats.map { format in
            let formatter = DateFormatter()
            formatter.dateFormat = format
            formatter.locale = Locale(identifier: "en_US_POSIX")
            formatter.timeZone = TimeZone(secondsFromGMT: 0)
            return formatter
        }
    }()

    private static let iso8601Formatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter
    }()

    private static let iso8601WithFractionalFormatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()

    static func parse(_ string: String) -> Date? {
        // Try ISO8601DateFormatter first (fastest)
        if let date = iso8601Formatter.date(from: string) {
            return date
        }
        if let date = iso8601WithFractionalFormatter.date(from: string) {
            return date
        }

        // Fallback to DateFormatter for edge cases
        for formatter in formatters {
            if let date = formatter.date(from: string) {
                return date
            }
        }

        return nil
    }
}

// MARK: - API Error

enum APIError: Error, LocalizedError {
    case invalidURL
    case networkError(Error)
    case decodingError(Error)
    case serverError(code: String, message: String)
    case driverDocsRequired(missingTypes: [DocumentType])
    case unauthorized
    case unknown

    var errorDescription: String? {
        switch self {
        case .invalidURL:
            return "Invalid URL"
        case .networkError(let error):
            return "Network error: \(error.localizedDescription)"
        case .decodingError(let error):
            return "Failed to decode response: \(error.localizedDescription)"
        case .serverError(_, let message):
            return message
        case .driverDocsRequired:
            return "Driver documents are required before switching to Driver mode."
        case .unauthorized:
            return "Please log in to continue"
        case .unknown:
            return "An unknown error occurred"
        }
    }

    var errorCode: String? {
        switch self {
        case .serverError(let code, _):
            return code
        case .driverDocsRequired:
            return APIErrorCode.driverDocsRequired.rawValue
        default:
            return nil
        }
    }
}

// MARK: - API Client Protocol

protocol APIClientProtocol {
    func register(request: RegisterRequest) async throws -> RegisterResponse
    func verifyEmail(request: VerifyEmailRequest) async throws -> MessageResponse
    func login(request: LoginRequest) async throws -> AuthTokens
    func refreshToken(request: RefreshTokenRequest) async throws -> AuthTokens
    func forgotPassword(request: ForgotPasswordRequest) async throws -> MessageResponse
    func resetPassword(request: ResetPasswordRequest) async throws -> MessageResponse
    func logout(request: RefreshTokenRequest) async throws -> MessageResponse
    func resendOTP(request: ResendOTPRequest) async throws -> MessageResponse

    // OTP passwordless login
    func requestLoginOTP(email: String) async throws -> MessageResponse
    func verifyLoginOTP(email: String, code: String) async throws -> OTPVerifyResponse
    func completeRegistrationWithToken(_ request: CompleteRegistrationRequest) async throws -> AuthTokens

    func getCurrentUser() async throws -> UserProfile
    func updateProfile(request: UpdateProfileRequest) async throws -> UpdateProfileResponse

    // Mode profiles (Owner / Driver switch)
    func listMyProfiles() async throws -> ListProfilesResponse
    func createMyProfile(role: UserRole) async throws -> ProfileSummary
    func switchActiveProfile(role: UserRole) async throws -> SwitchProfileResponse

    // Document methods
    func fetchDocuments() async throws -> [Document]
    func uploadDocument(type: DocumentType, fileData: Data, filename: String, mimeType: String) async throws -> Document
    func deleteDocument(id: UUID) async throws -> MessageResponse

    // Profile photo
    func uploadProfilePhoto(fileData: Data, filename: String, mimeType: String) async throws -> UserProfile

    // Onboarding
    func completeOnboarding() async throws -> MessageResponse

    // Cars (Owner)
    func fetchCars() async throws -> [Car]
    func getCar(id: UUID) async throws -> Car
    func createCar(request: CreateCarRequest) async throws -> Car
    func updateCar(id: UUID, request: UpdateCarRequest) async throws -> Car
    func deleteCar(id: UUID) async throws -> MessageResponse
    func togglePauseCar(id: UUID) async throws -> Car

    // Listings (Driver - Public)
    func fetchListings(status: String?, search: String?) async throws -> [Car]

    // Car photos
    func fetchCarPhotos(carId: UUID) async throws -> [CarPhotoAPIResponse]
    func uploadCarPhoto(carId: UUID, slotType: PhotoSlotType, fileData: Data, filename: String, mimeType: String) async throws -> CarPhotoAPIResponse
    func deleteCarPhoto(carId: UUID, photoId: UUID) async throws -> MessageResponse

    // Car documents
    func fetchCarDocuments(carId: UUID) async throws -> [CarDocumentAPIResponse]
    func uploadCarDocument(carId: UUID, documentType: CarDocumentType, fileData: Data, filename: String, mimeType: String) async throws -> CarDocumentAPIResponse
    func deleteCarDocument(carId: UUID, documentId: UUID) async throws -> MessageResponse

    // Car location (Owner)
    func updateCarLocation(carId: UUID, request: UpdateCarLocationRequest) async throws -> Car

    // Likes/Favorites
    func fetchLikedListings() async throws -> [UUID]
    func likeListing(id: UUID) async throws -> MessageResponse
    func unlikeListing(id: UUID) async throws -> MessageResponse

    // Chats
    func fetchChats(archived: Bool) async throws -> ChatsListAPIResponse
    func findOrCreateChat(request: FindOrCreateChatAPIRequest) async throws -> ChatAPIResponse
    func fetchMessages(chatId: UUID, cursor: String?, limit: Int) async throws -> MessagesPageAPIResponse
    func sendMessage(chatId: UUID, request: SendMessageAPIRequest) async throws -> ChatMessageAPIResponse
    func markChatRead(chatId: UUID) async throws -> MessageResponse
    func fetchChatRequests(chatId: UUID) async throws -> ChatRequestsListAPIResponse
    func createChatRequest(chatId: UUID, request: CreateChatRequestAPIRequest) async throws -> ChatRequestAPIResponse
    func respondToRequest(chatId: UUID, requestId: UUID, request: RespondToRequestAPIRequest) async throws -> ChatRequestAPIResponse
    func fetchChatDetails(chatId: UUID) async throws -> ChatDetailsAPIResponse
    func updateChatSettings(chatId: UUID, request: UpdateChatSettingsAPIRequest) async throws -> ChatDetailsAPIResponse
    func archiveChat(chatId: UUID, request: ArchiveChatAPIRequest) async throws -> MessageResponse
    func fetchChatAttachments(chatId: UUID, kind: String?) async throws -> AttachmentsListAPIResponse
    func uploadChatAttachment(chatId: UUID, fileData: Data, filename: String, mimeType: String) async throws -> ChatAttachmentAPIResponse
    func fetchCounterpartyProfile(userId: UUID) async throws -> CounterpartyProfileAPIResponse

    // Lease Requests
    func createLeaseRequest(listingId: UUID, request: CreateLeaseRequestAPIRequest) async throws -> CreateLeaseRequestAPIResponse
    func fetchLeaseRequests(chatId: UUID) async throws -> LeaseRequestsListAPIResponse
    func acceptLeaseRequest(id: UUID) async throws -> LeaseRequestAPIResponse
    func declineLeaseRequest(id: UUID) async throws -> LeaseRequestAPIResponse
    func cancelLeaseRequest(id: UUID) async throws -> LeaseRequestAPIResponse

    // Actions (Today tab) — chat requests
    func fetchMyActions() async throws -> ActionsListAPIResponse

    // Today actions (lease requests)
    func fetchTodayActions() async throws -> TodayActionsAPIResponse
    func markTodayActionsSeen() async throws -> MessageResponse

    // Payments (Stripe)
    func createPaymentIntent(leaseRequestId: UUID) async throws -> PaymentIntentAPIResponse
    func syncPaymentStatus(leaseRequestId: UUID) async throws -> LeaseRequestAPIResponse
}

// MARK: - API Client

final class APIClient: APIClientProtocol {
    static let shared = APIClient()

    private let baseURL: URL
    private let session: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder
    private let keychain: KeychainService

    private var isRefreshing = false
    private var refreshContinuations: [CheckedContinuation<Void, Error>] = []

    init(
        baseURL: URL = AppConfig.apiBaseURL,
        session: URLSession = .shared,
        keychain: KeychainService = .shared
    ) {
        self.baseURL = baseURL
        self.session = session
        self.keychain = keychain

        self.decoder = JSONDecoder()
        self.decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let dateString = try container.decode(String.self)
            guard let date = ISO8601DateParser.parse(dateString) else {
                throw DecodingError.dataCorruptedError(
                    in: container,
                    debugDescription: "Cannot decode date from: \(dateString)"
                )
            }
            return date
        }

        self.encoder = JSONEncoder()
        self.encoder.dateEncodingStrategy = .iso8601
    }

    // MARK: - Public API Methods

    func register(request: RegisterRequest) async throws -> RegisterResponse {
        try await post(path: "auth/register", body: request)
    }

    func verifyEmail(request: VerifyEmailRequest) async throws -> MessageResponse {
        try await post(path: "auth/verify-email", body: request)
    }

    func login(request: LoginRequest) async throws -> AuthTokens {
        try await post(path: "auth/login", body: request)
    }

    func refreshToken(request: RefreshTokenRequest) async throws -> AuthTokens {
        try await post(path: "auth/token/refresh", body: request)
    }

    func forgotPassword(request: ForgotPasswordRequest) async throws -> MessageResponse {
        try await post(path: "auth/password/forgot", body: request)
    }

    func resetPassword(request: ResetPasswordRequest) async throws -> MessageResponse {
        try await post(path: "auth/password/reset", body: request)
    }

    func logout(request: RefreshTokenRequest) async throws -> MessageResponse {
        try await post(path: "auth/logout", body: request)
    }

    func resendOTP(request: ResendOTPRequest) async throws -> MessageResponse {
        try await post(path: "auth/resend-otp", body: request)
    }

    func requestLoginOTP(email: String) async throws -> MessageResponse {
        try await post(path: "auth/otp/request", body: OTPLoginRequestBody(email: email))
    }

    func verifyLoginOTP(email: String, code: String) async throws -> OTPVerifyResponse {
        try await post(path: "auth/otp/verify", body: OTPVerifyRequestBody(email: email, code: code))
    }

    func completeRegistrationWithToken(_ request: CompleteRegistrationRequest) async throws -> AuthTokens {
        try await post(path: "auth/otp/complete-registration", body: request)
    }

    func getCurrentUser() async throws -> UserProfile {
        try await get(path: "me", authenticated: true)
    }

    func updateProfile(request: UpdateProfileRequest) async throws -> UpdateProfileResponse {
        try await patch(path: "profile", body: request, authenticated: true)
    }

    // MARK: - Mode Profile Methods

    func listMyProfiles() async throws -> ListProfilesResponse {
        try await get(path: "me/profiles", authenticated: true)
    }

    func createMyProfile(role: UserRole) async throws -> ProfileSummary {
        try await post(path: "me/profiles", body: CreateProfileRequest(role: role), authenticated: true)
    }

    func switchActiveProfile(role: UserRole) async throws -> SwitchProfileResponse {
        try await post(
            path: "me/active-profile",
            body: SwitchProfileRequest(role: role, profileId: nil),
            authenticated: true
        )
    }

    // MARK: - Document Methods

    func fetchDocuments() async throws -> [Document] {
        try await get(path: "documents", authenticated: true)
    }

    func uploadDocument(type: DocumentType, fileData: Data, filename: String, mimeType: String) async throws -> Document {
        try await uploadMultipart(
            path: "documents/\(type.rawValue)",
            fileData: fileData,
            filename: filename,
            mimeType: mimeType,
            authenticated: true
        )
    }

    func deleteDocument(id: UUID) async throws -> MessageResponse {
        try await delete(path: "documents/\(id.uuidString)", authenticated: true)
    }

    // MARK: - Profile Photo Methods

    func uploadProfilePhoto(fileData: Data, filename: String, mimeType: String) async throws -> UserProfile {
        try await uploadMultipart(
            path: "profile/photo",
            fileData: fileData,
            filename: filename,
            mimeType: mimeType,
            authenticated: true
        )
    }

    // MARK: - Onboarding Methods

    func completeOnboarding() async throws -> MessageResponse {
        try await postEmpty(path: "onboarding/complete", authenticated: true)
    }

    // MARK: - Car Methods

    func fetchCars() async throws -> [Car] {
        let response: CarsListResponse = try await get(path: "cars", authenticated: true)
        return response.cars.map { $0.toCar() }
    }

    func getCar(id: UUID) async throws -> Car {
        let response: CarAPIResponse = try await get(path: "cars/\(id.uuidString)", authenticated: true)
        return response.toCar()
    }

    func createCar(request: CreateCarRequest) async throws -> Car {
        let response: CarAPIResponse = try await post(path: "cars", body: request, authenticated: true)
        return response.toCar()
    }

    func updateCar(id: UUID, request: UpdateCarRequest) async throws -> Car {
        let response: CarAPIResponse = try await put(path: "cars/\(id.uuidString)", body: request, authenticated: true)
        return response.toCar()
    }

    func deleteCar(id: UUID) async throws -> MessageResponse {
        try await delete(path: "cars/\(id.uuidString)", authenticated: true)
    }

    func togglePauseCar(id: UUID) async throws -> Car {
        let response: CarAPIResponse = try await postEmpty(path: "cars/\(id.uuidString)/pause", authenticated: true)
        return response.toCar()
    }

    // MARK: - Car Location Methods

    func updateCarLocation(carId: UUID, request: UpdateCarLocationRequest) async throws -> Car {
        #if DEBUG
        print("[API] updateCarLocation carId=\(carId) lat=\(request.latitude) lng=\(request.longitude) area=\(request.area ?? "") street=\(request.street ?? "")")
        #endif
        let response: CarAPIResponse = try await put(path: "cars/\(carId.uuidString)/location", body: request, authenticated: true)
        return response.toCar()
    }

    // MARK: - Listings Methods (Public)

    func fetchListings(status: String? = nil, search: String? = nil) async throws -> [Car] {
        var path = "listings"
        var queryItems: [String] = []

        if let status = status, !status.isEmpty {
            queryItems.append("status=\(status)")
        }
        if let search = search, !search.isEmpty {
            queryItems.append("search=\(search.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? search)")
        }

        if !queryItems.isEmpty {
            path += "?" + queryItems.joined(separator: "&")
        }

        let response: ListingsResponse = try await get(path: path, authenticated: false)
        return response.listings.map { $0.toCar() }
    }

    // MARK: - Car Photo Methods

    func fetchCarPhotos(carId: UUID) async throws -> [CarPhotoAPIResponse] {
        let response: CarPhotosListResponse = try await get(path: "cars/\(carId.uuidString)/photos", authenticated: true)
        return response.photos
    }

    func uploadCarPhoto(carId: UUID, slotType: PhotoSlotType, fileData: Data, filename: String, mimeType: String) async throws -> CarPhotoAPIResponse {
        try await uploadMultipartWithFields(
            path: "cars/\(carId.uuidString)/photos",
            fileData: fileData,
            filename: filename,
            mimeType: mimeType,
            fields: ["slot_type": slotType.rawValue],
            authenticated: true
        )
    }

    func deleteCarPhoto(carId: UUID, photoId: UUID) async throws -> MessageResponse {
        try await delete(path: "cars/\(carId.uuidString)/photos/\(photoId.uuidString)", authenticated: true)
    }

    // MARK: - Car Document Methods

    func fetchCarDocuments(carId: UUID) async throws -> [CarDocumentAPIResponse] {
        let response: CarDocumentsListResponse = try await get(path: "cars/\(carId.uuidString)/documents", authenticated: true)
        return response.documents
    }

    func uploadCarDocument(carId: UUID, documentType: CarDocumentType, fileData: Data, filename: String, mimeType: String) async throws -> CarDocumentAPIResponse {
        try await uploadMultipartWithFields(
            path: "cars/\(carId.uuidString)/documents",
            fileData: fileData,
            filename: filename,
            mimeType: mimeType,
            fields: ["document_type": documentType.rawValue],
            authenticated: true
        )
    }

    func deleteCarDocument(carId: UUID, documentId: UUID) async throws -> MessageResponse {
        try await delete(path: "cars/\(carId.uuidString)/documents/\(documentId.uuidString)", authenticated: true)
    }

    // MARK: - Likes/Favorites Methods

    func fetchLikedListings() async throws -> [UUID] {
        let response: LikedListingsResponse = try await get(path: "me/likes", authenticated: true)
        return response.likedListingIds
    }

    func likeListing(id: UUID) async throws -> MessageResponse {
        try await postEmpty(path: "listings/\(id.uuidString)/like", authenticated: true)
    }

    func unlikeListing(id: UUID) async throws -> MessageResponse {
        try await delete(path: "listings/\(id.uuidString)/like", authenticated: true)
    }

    // MARK: - Chat Methods

    func fetchChats(archived: Bool = false) async throws -> ChatsListAPIResponse {
        try await get(path: "chats?archived=\(archived)", authenticated: true)
    }

    func findOrCreateChat(request: FindOrCreateChatAPIRequest) async throws -> ChatAPIResponse {
        try await post(path: "chats", body: request, authenticated: true)
    }

    func fetchMessages(chatId: UUID, cursor: String? = nil, limit: Int = 30) async throws -> MessagesPageAPIResponse {
        var path = "chats/\(chatId.uuidString)/messages?limit=\(limit)"
        if let cursor = cursor {
            path += "&cursor=\(cursor)"
        }
        return try await get(path: path, authenticated: true)
    }

    func sendMessage(chatId: UUID, request: SendMessageAPIRequest) async throws -> ChatMessageAPIResponse {
        try await post(path: "chats/\(chatId.uuidString)/messages", body: request, authenticated: true)
    }

    func markChatRead(chatId: UUID) async throws -> MessageResponse {
        try await postEmpty(path: "chats/\(chatId.uuidString)/read", authenticated: true)
    }

    func fetchChatRequests(chatId: UUID) async throws -> ChatRequestsListAPIResponse {
        try await get(path: "chats/\(chatId.uuidString)/requests", authenticated: true)
    }

    func createChatRequest(chatId: UUID, request: CreateChatRequestAPIRequest) async throws -> ChatRequestAPIResponse {
        try await post(path: "chats/\(chatId.uuidString)/requests", body: request, authenticated: true)
    }

    func respondToRequest(chatId: UUID, requestId: UUID, request: RespondToRequestAPIRequest) async throws -> ChatRequestAPIResponse {
        try await post(path: "chats/\(chatId.uuidString)/requests/\(requestId.uuidString)/respond", body: request, authenticated: true)
    }

    func fetchChatDetails(chatId: UUID) async throws -> ChatDetailsAPIResponse {
        try await get(path: "chats/\(chatId.uuidString)/details", authenticated: true)
    }

    func updateChatSettings(chatId: UUID, request: UpdateChatSettingsAPIRequest) async throws -> ChatDetailsAPIResponse {
        try await patch(path: "chats/\(chatId.uuidString)/settings", body: request, authenticated: true)
    }

    func archiveChat(chatId: UUID, request: ArchiveChatAPIRequest) async throws -> MessageResponse {
        try await post(path: "chats/\(chatId.uuidString)/archive", body: request, authenticated: true)
    }

    func fetchChatAttachments(chatId: UUID, kind: String? = nil) async throws -> AttachmentsListAPIResponse {
        var path = "chats/\(chatId.uuidString)/attachments"
        if let kind = kind {
            path += "?kind=\(kind)"
        }
        return try await get(path: path, authenticated: true)
    }

    func uploadChatAttachment(chatId: UUID, fileData: Data, filename: String, mimeType: String) async throws -> ChatAttachmentAPIResponse {
        try await uploadMultipart(
            path: "chats/\(chatId.uuidString)/attachments",
            fileData: fileData, filename: filename, mimeType: mimeType,
            authenticated: true
        )
    }

    func fetchCounterpartyProfile(userId: UUID) async throws -> CounterpartyProfileAPIResponse {
        try await get(path: "users/\(userId.uuidString)/profile", authenticated: true)
    }

    // MARK: - Actions (Today tab)

    func fetchMyActions() async throws -> ActionsListAPIResponse {
        try await get(path: "me/actions", authenticated: true)
    }

    // MARK: - Today Actions (Lease Requests)

    func fetchTodayActions() async throws -> TodayActionsAPIResponse {
        try await get(path: "today/actions", authenticated: true)
    }

    func markTodayActionsSeen() async throws -> MessageResponse {
        try await postEmpty(path: "today/actions/seen", authenticated: true)
    }

    // MARK: - Lease Request Methods

    func createLeaseRequest(listingId: UUID, request: CreateLeaseRequestAPIRequest) async throws -> CreateLeaseRequestAPIResponse {
        try await post(path: "listings/\(listingId.uuidString)/lease-requests", body: request, authenticated: true)
    }

    func fetchLeaseRequests(chatId: UUID) async throws -> LeaseRequestsListAPIResponse {
        try await get(path: "chats/\(chatId.uuidString)/lease-requests", authenticated: true)
    }

    func acceptLeaseRequest(id: UUID) async throws -> LeaseRequestAPIResponse {
        try await postEmpty(path: "lease-requests/\(id.uuidString)/accept", authenticated: true)
    }

    func declineLeaseRequest(id: UUID) async throws -> LeaseRequestAPIResponse {
        try await postEmpty(path: "lease-requests/\(id.uuidString)/decline", authenticated: true)
    }

    func cancelLeaseRequest(id: UUID) async throws -> LeaseRequestAPIResponse {
        try await postEmpty(path: "lease-requests/\(id.uuidString)/cancel", authenticated: true)
    }

    func createPaymentIntent(leaseRequestId: UUID) async throws -> PaymentIntentAPIResponse {
        try await postEmpty(path: "lease-requests/\(leaseRequestId.uuidString)/payments/intent", authenticated: true)
    }

    func syncPaymentStatus(leaseRequestId: UUID) async throws -> LeaseRequestAPIResponse {
        try await postEmpty(path: "lease-requests/\(leaseRequestId.uuidString)/payments/sync", authenticated: true)
    }

    // MARK: - Private Request Methods

    private func post<T: Codable, R: Codable>(
        path: String,
        body: T,
        authenticated: Bool = false
    ) async throws -> R {
        var request = try makeRequest(path: path, method: "POST")
        request.httpBody = try encoder.encode(body)

        if authenticated {
            try await addAuthHeader(to: &request)
        }

        return try await execute(request: request, authenticated: authenticated)
    }

    private func get<R: Codable>(
        path: String,
        authenticated: Bool = false
    ) async throws -> R {
        var request = try makeRequest(path: path, method: "GET")

        if authenticated {
            try await addAuthHeader(to: &request)
        }

        return try await execute(request: request, authenticated: authenticated)
    }

    private func patch<T: Codable, R: Codable>(
        path: String,
        body: T,
        authenticated: Bool = false
    ) async throws -> R {
        var request = try makeRequest(path: path, method: "PATCH")
        request.httpBody = try encoder.encode(body)

        if authenticated {
            try await addAuthHeader(to: &request)
        }

        return try await execute(request: request, authenticated: authenticated)
    }

    private func put<T: Codable, R: Codable>(
        path: String,
        body: T,
        authenticated: Bool = false
    ) async throws -> R {
        var request = try makeRequest(path: path, method: "PUT")
        request.httpBody = try encoder.encode(body)

        if authenticated {
            try await addAuthHeader(to: &request)
        }

        return try await execute(request: request, authenticated: authenticated)
    }

    private func delete<R: Codable>(
        path: String,
        authenticated: Bool = false
    ) async throws -> R {
        var request = try makeRequest(path: path, method: "DELETE")

        if authenticated {
            try await addAuthHeader(to: &request)
        }

        return try await execute(request: request, authenticated: authenticated)
    }

    private func postEmpty<R: Codable>(
        path: String,
        authenticated: Bool = false
    ) async throws -> R {
        var request = try makeRequest(path: path, method: "POST")

        if authenticated {
            try await addAuthHeader(to: &request)
        }

        return try await execute(request: request, authenticated: authenticated)
    }

    private func uploadMultipart<R: Codable>(
        path: String,
        fileData: Data,
        filename: String,
        mimeType: String,
        authenticated: Bool = false
    ) async throws -> R {
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw APIError.invalidURL
        }

        let boundary = UUID().uuidString
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")

        if authenticated {
            try await addAuthHeader(to: &request)
        }

        // Build multipart body
        var body = Data()
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n".data(using: .utf8)!)
        body.append("Content-Type: \(mimeType)\r\n\r\n".data(using: .utf8)!)
        body.append(fileData)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)

        request.httpBody = body

        return try await execute(request: request, authenticated: authenticated)
    }

    private func uploadMultipartWithFields<R: Codable>(
        path: String,
        fileData: Data,
        filename: String,
        mimeType: String,
        fields: [String: String],
        authenticated: Bool = false
    ) async throws -> R {
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw APIError.invalidURL
        }

        let boundary = UUID().uuidString
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")

        if authenticated {
            try await addAuthHeader(to: &request)
        }

        // Build multipart body
        var body = Data()

        // Add text fields
        for (key, value) in fields {
            body.append("--\(boundary)\r\n".data(using: .utf8)!)
            body.append("Content-Disposition: form-data; name=\"\(key)\"\r\n\r\n".data(using: .utf8)!)
            body.append("\(value)\r\n".data(using: .utf8)!)
        }

        // Add file
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n".data(using: .utf8)!)
        body.append("Content-Type: \(mimeType)\r\n\r\n".data(using: .utf8)!)
        body.append(fileData)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)

        request.httpBody = body

        return try await execute(request: request, authenticated: authenticated)
    }

    private func makeRequest(path: String, method: String) throws -> URLRequest {
        guard let url = URL(string: path, relativeTo: baseURL) else {
            throw APIError.invalidURL
        }

        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")

        return request
    }

    private func addAuthHeader(to request: inout URLRequest) async throws {
        // Check if token is expired and needs refresh
        if keychain.isAccessTokenExpired() {
            try await refreshAccessToken()
        }

        guard let token = keychain.getAccessToken() else {
            throw APIError.unauthorized
        }

        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    }

    private func execute<R: Codable>(
        request: URLRequest,
        authenticated: Bool,
        retryCount: Int = 0
    ) async throws -> R {
        #if DEBUG
        print("[API] \(request.httpMethod ?? "?") \(request.url?.absoluteString ?? "nil")")
        if let body = request.httpBody, let bodyStr = String(data: body, encoding: .utf8) {
            print("[API] Body: \(bodyStr)")
        }
        #endif

        do {
            let (data, response) = try await session.data(for: request)

            guard let httpResponse = response as? HTTPURLResponse else {
                throw APIError.unknown
            }

            #if DEBUG
            print("[API] Response: \(httpResponse.statusCode)")
            if let responseStr = String(data: data, encoding: .utf8) {
                print("[API] Response body: \(responseStr)")
            }
            #endif

            // Handle 401 with token refresh (only once)
            if httpResponse.statusCode == 401 && authenticated && retryCount == 0 {
                try await refreshAccessToken()

                // Retry the request with new token
                var newRequest = request
                if let token = keychain.getAccessToken() {
                    newRequest.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
                }
                return try await execute(request: newRequest, authenticated: authenticated, retryCount: 1)
            }

            // Handle error responses
            if httpResponse.statusCode >= 400 {
                #if DEBUG
                print("[API] ERROR: Status \(httpResponse.statusCode) for \(request.url?.absoluteString ?? "?")")
                #endif
                if let errorResponse = try? decoder.decode(APIErrorResponse.self, from: data) {
                    // Promote DRIVER_DOCS_REQUIRED to its own typed case so
                    // callers (ProfileView) can present the doc-upload flow
                    // without string-matching the code.
                    if errorResponse.error.code == APIErrorCode.driverDocsRequired.rawValue {
                        let missingRaw = errorResponse.error.details?.missingTypes ?? []
                        let missing = missingRaw.compactMap { DocumentType(rawValue: $0) }
                        throw APIError.driverDocsRequired(missingTypes: missing)
                    }
                    throw APIError.serverError(
                        code: errorResponse.error.code,
                        message: errorResponse.error.message
                    )
                }
                throw APIError.serverError(code: "UNKNOWN", message: "Request failed with status \(httpResponse.statusCode)")
            }

            // Decode success response
            do {
                return try decoder.decode(R.self, from: data)
            } catch {
                #if DEBUG
                print("[API] Decode error: \(error)")
                #endif
                throw APIError.decodingError(error)
            }

        } catch let error as APIError {
            throw error
        } catch {
            #if DEBUG
            print("[API] Network error: \(error)")
            #endif
            throw APIError.networkError(error)
        }
    }

    // MARK: - Token Refresh

    private func refreshAccessToken() async throws {
        // If already refreshing, wait for it to complete
        if isRefreshing {
            try await withCheckedThrowingContinuation { continuation in
                refreshContinuations.append(continuation)
            }
            return
        }

        isRefreshing = true
        defer {
            isRefreshing = false
            // Resume all waiting continuations
            let continuations = refreshContinuations
            refreshContinuations.removeAll()
            for continuation in continuations {
                continuation.resume()
            }
        }

        guard let refreshToken = keychain.getRefreshToken() else {
            throw APIError.unauthorized
        }

        let request = RefreshTokenRequest(refreshToken: refreshToken)
        let tokens: AuthTokens = try await post(path: "auth/token/refresh", body: request)

        try keychain.saveTokens(
            accessToken: tokens.accessToken,
            refreshToken: tokens.refreshToken,
            expiresAt: tokens.expiresAt
        )
    }
}
