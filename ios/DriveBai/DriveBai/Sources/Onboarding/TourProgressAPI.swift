import Foundation

// MARK: - Onboarding-progress wire models
//
// Matches the backend wire shape exactly (snake_case keys, nullable
// `last_step_key`). The server is the source of truth for tour progress; a
// local cache (see OnboardingProgressService) mirrors it for offline / optimistic
// starts. `user_id` is never sent — the handlers derive it from the JWT
// subject, so there is no IDOR surface.

/// One row of `user_onboarding_progress` as returned by GET.
struct TourProgressDTO: Codable, Equatable {
    let tourKey: String
    let status: String
    let lastStepKey: String?
    let updatedAt: Date?

    enum CodingKeys: String, CodingKey {
        case tourKey     = "tour_key"
        case status
        case lastStepKey = "last_step_key"
        case updatedAt   = "updated_at"
    }
}

/// Request body for the PUT upsert. Carries the role (validated server-side
/// against the caller's `user_profiles`) but never a user_id.
struct TourProgressUpsertRequest: Codable {
    let role: String
    let tourKey: String
    let status: String
    let lastStepKey: String?

    enum CodingKeys: String, CodingKey {
        case role
        case tourKey     = "tour_key"
        case status
        case lastStepKey = "last_step_key"
    }
}

/// Tolerant decode for GET — accepts either a bare array (the documented shape)
/// or an object wrapping the rows under `progress`.
struct TourProgressListResponse: Decodable {
    let items: [TourProgressDTO]

    init(from decoder: Decoder) throws {
        if let arr = try? [TourProgressDTO](from: decoder) {
            items = arr
            return
        }
        let container = try decoder.container(keyedBy: CodingKeys.self)
        items = (try? container.decode([TourProgressDTO].self, forKey: .progress)) ?? []
    }

    enum CodingKeys: String, CodingKey {
        case progress
    }
}

// MARK: - APIClient endpoints
//
// Implemented as an extension with self-contained request helpers, mirroring
// the APIClient+Purchase pattern (the core client's private helpers are
// file-scoped and invisible here). These calls are best-effort from the tour's
// point of view — the coordinator writes the local cache first and treats a
// failed round-trip as "retry on next launch".

extension APIClient {

    /// GET /api/v1/me/onboarding-progress[?role=driver] — all rows for the
    /// caller (scoped to the JWT subject).
    func fetchOnboardingProgress(role: TourRole? = nil) async throws -> [TourProgressDTO] {
        var path = "me/onboarding-progress"
        if let role {
            path += "?role=\(role.rawValue)"
        }
        let resp: TourProgressListResponse = try await tourAuthedRequest(
            path: path, method: "GET", jsonBody: nil
        )
        return resp.items
    }

    /// PUT /api/v1/me/onboarding-progress — idempotent upsert of one tour row.
    @discardableResult
    func putOnboardingProgress(
        role: TourRole,
        tourKey: TourKey,
        status: TourStatus,
        lastStepKey: String?
    ) async throws -> TourProgressDTO {
        let body = TourProgressUpsertRequest(
            role: role.rawValue,
            tourKey: tourKey.rawValue,
            status: status.rawValue,
            lastStepKey: lastStepKey
        )
        let data = try tourEncoder().encode(body)
        return try await tourAuthedRequest(path: "me/onboarding-progress", method: "PUT", jsonBody: data)
    }

    /// DELETE /api/v1/me/onboarding-progress — DEBUG reset. Clears every row for
    /// the caller. Returns the server message.
    @discardableResult
    func deleteOnboardingProgress() async throws -> MessageResponse {
        try await tourAuthedRequest(path: "me/onboarding-progress", method: "DELETE", jsonBody: nil)
    }

    // MARK: Private helpers (self-contained, matching APIClient+Purchase)

    private func tourAuthedRequest<R: Decodable>(
        path: String,
        method: String,
        jsonBody: Data?
    ) async throws -> R {
        guard let url = URL(string: path, relativeTo: AppConfig.apiBaseURL) else {
            throw APIError.invalidURL
        }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.httpBody = jsonBody

        await self.ensureFreshToken()
        guard let token = KeychainService.shared.getAccessToken() else {
            throw APIError.unauthorized
        }
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

        do {
            let (data, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse else { throw APIError.unknown }
            if http.statusCode >= 400 {
                if let err = try? tourDecoder().decode(APIErrorResponse.self, from: data) {
                    throw APIError.serverError(code: err.error.code, message: err.error.message)
                }
                throw APIError.serverError(
                    code: "UNKNOWN",
                    message: "Request failed with status \(http.statusCode)"
                )
            }
            do {
                return try tourDecoder().decode(R.self, from: data)
            } catch {
                throw APIError.decodingError(error)
            }
        } catch let error as APIError {
            throw error
        } catch {
            throw APIError.networkError(error)
        }
    }

    private func tourEncoder() -> JSONEncoder {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        return encoder
    }

    private func tourDecoder() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
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
        return decoder
    }
}
