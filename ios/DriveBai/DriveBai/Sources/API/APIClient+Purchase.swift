import Foundation

// MARK: - Purchase Endpoints
//
// Extension keeps all Buy-the-Car networking in one place without touching
// the core APIClient. The private helpers `post`, `get`, `patch`,
// `postEmpty`, `uploadMultipart`, and `uploadMultipartWithFields` are
// invisible from here, so we reimplement thin wrappers that go through
// the public path — using URLRequest + URLSession directly would fork the
// error handling.  Instead we build lightweight helpers by leaning on the
// public `execute`-style methods indirectly through `sendPurchase`.

extension APIClient {
    // MARK: - Buyer

    /// POST /cars/{id}/purchase-requests — backend returns the newly
    /// created purchase row directly (same shape as GET
    /// /purchase-requests/{id}). No wrapper envelope.
    func createPurchaseRequest(
        carId: UUID,
        offerAmountCents: Int64,
        buyerMessage: String?
    ) async throws -> PurchaseRequestAPIResponse {
        let body = CreatePurchaseRequestAPIRequest(
            offerAmountCents: offerAmountCents,
            buyerMessage: buyerMessage
        )
        return try await purchasePost(
            path: "cars/\(carId.uuidString)/purchase-requests",
            body: body
        )
    }

    /// GET /purchase-requests/{id}
    func fetchPurchaseRequest(id: UUID) async throws -> PurchaseRequestAPIResponse {
        try await purchaseGet(path: "purchase-requests/\(id.uuidString)")
    }

    /// GET /chats/{chatId}/purchase-requests
    func fetchPurchaseRequestsForChat(chatId: UUID) async throws -> PurchaseRequestsListAPIResponse {
        try await purchaseGet(path: "chats/\(chatId.uuidString)/purchase-requests")
    }

    /// POST /purchase-requests/{id}/cancel — buyer withdraws before payment.
    func cancelPurchaseRequest(id: UUID) async throws -> PurchaseRequestAPIResponse {
        try await purchasePostEmpty(path: "purchase-requests/\(id.uuidString)/cancel")
    }

    /// POST /purchase-requests/{id}/inspect/accept — buyer confirms the car.
    /// Requires the full inspection checklist (every field true); the server
    /// additionally gates on a title document (409 TITLE_REQUIRED) and a BoS
    /// title_condition (400 INSPECTION_CHECKLIST_INCOMPLETE) before it
    /// captures payment.
    func buyerAcceptVehicle(
        purchaseRequestId: UUID,
        checklist: InspectVehicleAcceptAPIRequest
    ) async throws -> PurchaseRequestAPIResponse {
        try await purchasePost(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/inspect/accept",
            body: checklist
        )
    }

    /// POST /purchase-requests/{id}/inspect/reject — buyer flags a problem
    /// and hands the case to admin adjudication.
    func buyerRejectVehicle(
        purchaseRequestId: UUID,
        reason: PurchaseRejectionReason,
        explanation: String,
        evidenceIds: [UUID]
    ) async throws -> PurchaseRejectionAPIResponse {
        let body = RejectVehicleAPIRequest(
            reasonCategory: reason.rawValue,
            explanation: explanation,
            evidenceIds: evidenceIds
        )
        return try await purchasePost(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/inspect/reject",
            body: body
        )
    }

    /// POST /purchase-requests/{id}/rejection-evidence — multipart. Uploads
    /// a single file at a time; callers loop for multi-file batches.
    func uploadRejectionEvidence(
        purchaseRequestId: UUID,
        fileData: Data,
        filename: String,
        mimeType: String
    ) async throws -> PurchaseRejectionEvidenceAPIResponse {
        try await purchaseUploadMultipart(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/rejection-evidence",
            fileData: fileData,
            filename: filename,
            mimeType: mimeType
        )
    }

    // MARK: - Bill of Sale

    /// GET /purchase-requests/{id}/bos — fetches the current BoS row
    /// (seeded when seller accepted the offer). Callers use this on
    /// BillOfSaleFlowView.onAppear so the wizard has a valid id to
    /// PATCH against — without it, every "Save & continue" was a silent
    /// no-op.
    func getBillOfSale(purchaseRequestId: UUID) async throws -> BillOfSaleAPIResponse {
        try await purchaseGet(path: "purchase-requests/\(purchaseRequestId.uuidString)/bos")
    }

    /// PATCH /purchase-requests/{id}/bos — seller-only. Any non-nil
    /// field on the request body is written; buyer identity fields must
    /// go through updateBillOfSaleBuyerFields instead (that path is
    /// buyer-only, this one 403s the buyer).
    func updateBillOfSale(
        purchaseRequestId: UUID,
        request: UpdateBillOfSaleAPIRequest
    ) async throws -> BillOfSaleAPIResponse {
        try await purchasePatch(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/bos",
            body: request
        )
    }

    /// PATCH /purchase-requests/{id}/bos/buyer-fields — buyer-only.
    /// Backend routes buyerName / buyerAddress through this dedicated
    /// endpoint; hitting /bos with them returns 403 for any non-seller.
    func updateBillOfSaleBuyerFields(
        purchaseRequestId: UUID,
        request: UpdateBillOfSaleBuyerFieldsAPIRequest
    ) async throws -> BillOfSaleAPIResponse {
        try await purchasePatch(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/bos/buyer-fields",
            body: request
        )
    }

    /// POST /purchase-requests/{id}/bos/sign — multipart PNG signature +
    /// `role` form field.  Role must be "buyer" or "seller".
    func signBillOfSale(
        purchaseRequestId: UUID,
        role: String,
        signatureData: Data
    ) async throws -> BillOfSaleAPIResponse {
        try await purchaseUploadMultipartWithFields(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/bos/sign",
            fileData: signatureData,
            filename: "signature.png",
            mimeType: "image/png",
            fields: ["role": role]
        )
    }

    /// Convenience wrapper — buyer signs.
    func buyerSignBillOfSale(
        purchaseRequestId: UUID,
        signatureData: Data
    ) async throws -> BillOfSaleAPIResponse {
        try await signBillOfSale(
            purchaseRequestId: purchaseRequestId,
            role: "buyer",
            signatureData: signatureData
        )
    }

    /// Convenience wrapper — seller signs.
    func sellerSignBillOfSale(
        purchaseRequestId: UUID,
        signatureData: Data
    ) async throws -> BillOfSaleAPIResponse {
        try await signBillOfSale(
            purchaseRequestId: purchaseRequestId,
            role: "seller",
            signatureData: signatureData
        )
    }

    // MARK: - Payment

    /// POST /purchase-requests/{id}/payment-intent — creates the
    /// manual-capture PaymentIntent. The response shape mirrors the lease
    /// PaymentSheet flow so we can reuse `PaymentIntentAPIResponse`.
    func createPurchasePaymentIntent(
        purchaseRequestId: UUID
    ) async throws -> PaymentIntentAPIResponse {
        try await purchasePostEmpty(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/payment-intent"
        )
    }

    /// POST /purchase-requests/{id}/sync-payment — force a Stripe → backend
    /// reconciliation when the webhook lags.
    func syncPurchasePayment(
        purchaseRequestId: UUID
    ) async throws -> PurchaseRequestAPIResponse {
        try await purchasePostEmpty(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/sync-payment"
        )
    }

    // MARK: - Seller

    /// POST /purchase-requests/{id}/accept — owner accepts the offer.
    func acceptPurchaseRequest(id: UUID) async throws -> PurchaseRequestAPIResponse {
        try await purchasePostEmpty(path: "purchase-requests/\(id.uuidString)/accept")
    }

    /// POST /purchase-requests/{id}/decline — owner rejects the offer.
    func declinePurchaseRequest(id: UUID, reason: String?) async throws -> PurchaseRequestAPIResponse {
        let body = DeclinePurchaseAPIRequest(reason: reason)
        return try await purchasePost(
            path: "purchase-requests/\(id.uuidString)/decline",
            body: body
        )
    }

    /// POST /purchase-requests/{id}/schedule-handover — seller picks a
    /// meeting time + location.
    func scheduleHandover(
        purchaseRequestId: UUID,
        scheduledAt: Date,
        location: String,
        latitude: Double?,
        longitude: Double?
    ) async throws -> PurchaseRequestAPIResponse {
        let body = ScheduleHandoverAPIRequest(
            handoverScheduledAt: scheduledAt,
            handoverLocation: location,
            handoverLatitude: latitude,
            handoverLongitude: longitude
        )
        return try await purchasePost(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/schedule-handover",
            body: body
        )
    }

    /// POST /purchase-requests/{id}/keys-handed-over — seller confirms the
    /// in-person handover happened.
    func confirmKeysHandedOver(
        purchaseRequestId: UUID
    ) async throws -> PurchaseRequestAPIResponse {
        try await purchasePostEmpty(
            path: "purchase-requests/\(purchaseRequestId.uuidString)/keys-handed-over"
        )
    }

    // MARK: - Today feed

    /// GET /today/actions — same endpoint the lease/return flows already
    /// use.  Purchase rows are appended by the backend on the same list.
    /// The wrapper decodes into `PurchaseRequestsListAPIResponse` so
    /// callers get a typed shape for the purchase-only case; the raw
    /// `today/actions` payload has broader coverage.
    func fetchPurchaseRequestsToday() async throws -> PurchaseRequestsListAPIResponse {
        try await purchaseGet(path: "today/purchase-requests")
    }

    // MARK: - Private HTTP helpers
    //
    // We can't reach APIClient's private post/get/patch/etc., so we
    // reconstruct them here.  Each helper mirrors the private version 1:1:
    // build request, attach bearer, run through `URLSession.data(for:)`,
    // decode the response with the shared decoder configured on the
    // instance.

    private func purchaseGet<R: Decodable>(path: String) async throws -> R {
        try await runAuthenticatedRequest(path: path, method: "GET", jsonBody: nil)
    }

    private func purchasePost<T: Encodable, R: Decodable>(path: String, body: T) async throws -> R {
        let jsonData = try purchaseEncoder().encode(body)
        return try await runAuthenticatedRequest(path: path, method: "POST", jsonBody: jsonData)
    }

    private func purchasePatch<T: Encodable, R: Decodable>(path: String, body: T) async throws -> R {
        let jsonData = try purchaseEncoder().encode(body)
        return try await runAuthenticatedRequest(path: path, method: "PATCH", jsonBody: jsonData)
    }

    private func purchasePostEmpty<R: Decodable>(path: String) async throws -> R {
        try await runAuthenticatedRequest(path: path, method: "POST", jsonBody: nil)
    }

    private func purchaseUploadMultipart<R: Decodable>(
        path: String, fileData: Data, filename: String, mimeType: String
    ) async throws -> R {
        try await runMultipartRequest(
            path: path,
            fileData: fileData,
            filename: filename,
            mimeType: mimeType,
            fields: [:]
        )
    }

    private func purchaseUploadMultipartWithFields<R: Decodable>(
        path: String, fileData: Data, filename: String, mimeType: String, fields: [String: String]
    ) async throws -> R {
        try await runMultipartRequest(
            path: path,
            fileData: fileData,
            filename: filename,
            mimeType: mimeType,
            fields: fields
        )
    }

    // MARK: - Runners

    private func runAuthenticatedRequest<R: Decodable>(
        path: String,
        method: String,
        jsonBody: Data?
    ) async throws -> R {
        var request = try makePurchaseRequest(path: path, method: method)
        request.httpBody = jsonBody
        try await attachAuthHeader(&request)
        return try await executePurchaseRequest(request: request)
    }

    private func runMultipartRequest<R: Decodable>(
        path: String,
        fileData: Data,
        filename: String,
        mimeType: String,
        fields: [String: String]
    ) async throws -> R {
        var request = try makePurchaseRequest(path: path, method: "POST", jsonContentType: false)
        let boundary = UUID().uuidString
        request.setValue(
            "multipart/form-data; boundary=\(boundary)",
            forHTTPHeaderField: "Content-Type"
        )
        try await attachAuthHeader(&request)

        var body = Data()
        for (key, value) in fields {
            body.append("--\(boundary)\r\n".data(using: .utf8)!)
            body.append("Content-Disposition: form-data; name=\"\(key)\"\r\n\r\n".data(using: .utf8)!)
            body.append("\(value)\r\n".data(using: .utf8)!)
        }
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append(
            "Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n"
                .data(using: .utf8)!
        )
        body.append("Content-Type: \(mimeType)\r\n\r\n".data(using: .utf8)!)
        body.append(fileData)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)
        request.httpBody = body

        return try await executePurchaseRequest(request: request)
    }

    private func makePurchaseRequest(
        path: String,
        method: String,
        jsonContentType: Bool = true
    ) throws -> URLRequest {
        guard let url = URL(string: path, relativeTo: AppConfig.apiBaseURL) else {
            throw APIError.invalidURL
        }
        var request = URLRequest(url: url)
        request.httpMethod = method
        if jsonContentType {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        return request
    }

    private func attachAuthHeader(_ request: inout URLRequest) async throws {
        // Refresh only if expired — mirrors the core client's cadence.
        await self.ensureFreshToken()
        guard let token = KeychainService.shared.getAccessToken() else {
            throw APIError.unauthorized
        }
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    }

    private func executePurchaseRequest<R: Decodable>(request: URLRequest) async throws -> R {
        do {
            let (data, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse else {
                throw APIError.unknown
            }
            if http.statusCode >= 400 {
                if let errorResponse = try? purchaseDecoder().decode(APIErrorResponse.self, from: data) {
                    throw APIError.serverError(
                        code: errorResponse.error.code,
                        message: errorResponse.error.message
                    )
                }
                throw APIError.serverError(
                    code: "UNKNOWN",
                    message: "Request failed with status \(http.statusCode)"
                )
            }
            do {
                return try purchaseDecoder().decode(R.self, from: data)
            } catch {
                throw APIError.decodingError(error)
            }
        } catch let error as APIError {
            throw error
        } catch {
            throw APIError.networkError(error)
        }
    }

    // Local encoder/decoder that mirror the core client's configuration.
    private func purchaseEncoder() -> JSONEncoder {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        return encoder
    }

    private func purchaseDecoder() -> JSONDecoder {
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
