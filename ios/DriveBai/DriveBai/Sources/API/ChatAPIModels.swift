import Foundation

// MARK: - API Response Models

struct ChatsListAPIResponse: Codable {
    let chats: [ChatSummaryAPIResponse]
    let totalUnread: Int

    enum CodingKeys: String, CodingKey {
        case chats
        case totalUnread = "total_unread"
    }
}

struct ChatSummaryAPIResponse: Codable {
    let id: UUID
    let carId: UUID
    let carTitle: String
    let carCoverPhotoUrl: String?
    let counterpartyId: UUID
    let counterpartyName: String
    let counterpartyAvatarUrl: String?
    let lastMessage: String?
    let lastMessageAt: Date?
    let unreadCount: Int
    let openRequestsCount: Int
    let isArchived: Bool

    enum CodingKeys: String, CodingKey {
        case id
        case carId = "car_id"
        case carTitle = "car_title"
        case carCoverPhotoUrl = "car_cover_photo_url"
        case counterpartyId = "counterparty_id"
        case counterpartyName = "counterparty_name"
        case counterpartyAvatarUrl = "counterparty_avatar_url"
        case lastMessage = "last_message"
        case lastMessageAt = "last_message_at"
        case unreadCount = "unread_count"
        case openRequestsCount = "open_requests_count"
        case isArchived = "is_archived"
    }

    func toChatSummary() -> ChatSummary {
        ChatSummary(
            id: id, carId: carId, carTitle: carTitle,
            carCoverPhotoURL: carCoverPhotoUrl,
            counterpartyId: counterpartyId, counterpartyName: counterpartyName,
            counterpartyAvatarURL: counterpartyAvatarUrl,
            lastMessage: lastMessage, lastMessageAt: lastMessageAt,
            unreadCount: unreadCount, openRequestsCount: openRequestsCount,
            isArchived: isArchived
        )
    }
}

struct MessagesPageAPIResponse: Codable {
    let messages: [ChatMessageAPIResponse]
    let nextCursor: String?
    let hasMore: Bool

    enum CodingKeys: String, CodingKey {
        case messages
        case nextCursor = "next_cursor"
        case hasMore = "has_more"
    }
}

struct ChatMessageAPIResponse: Codable {
    let id: UUID
    let chatId: UUID
    let senderId: UUID
    let senderName: String
    let senderKind: String?
    let type: String
    let body: String
    let attachments: [ChatAttachmentAPIResponse]?
    let clientMessageId: UUID?
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case chatId = "chat_id"
        case senderId = "sender_id"
        case senderName = "sender_name"
        case senderKind = "sender_kind"
        case type, body, attachments
        case clientMessageId = "client_message_id"
        case createdAt = "created_at"
    }

    func toChatMessage(currentUserId: UUID) -> ChatMessage {
        ChatMessage(
            id: id,
            clientMessageId: clientMessageId ?? id,
            chatId: chatId,
            senderId: senderId,
            senderName: senderName,
            senderKind: senderKind ?? "user",
            direction: senderId == currentUserId ? .sent : .received,
            messageType: type,
            body: body,
            attachments: (attachments ?? []).map { $0.toAttachment() },
            createdAt: createdAt,
            status: .sent
        )
    }
}

struct ChatAttachmentAPIResponse: Codable {
    let id: UUID
    let kind: String
    let filename: String
    let mimeType: String
    let fileSize: Int
    let fileUrl: String
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id, kind, filename
        case mimeType = "mime_type"
        case fileSize = "file_size"
        case fileUrl = "file_url"
        case createdAt = "created_at"
    }

    func toAttachment() -> ChatAttachment {
        ChatAttachment(
            id: id,
            kind: AttachmentType(rawValue: kind) ?? .document,
            filename: filename,
            fileURL: fileUrl,
            fileSize: fileSize,
            mimeType: mimeType
        )
    }
}

struct ChatRequestAPIResponse: Codable {
    let id: UUID
    let chatId: UUID
    let type: String
    let status: String
    let createdById: UUID
    let createdByName: String
    let targetUserId: UUID
    let title: String
    let description: String
    let amount: Double?
    let currency: String
    let attachments: [ChatAttachmentAPIResponse]?
    let expiresAt: Date
    let createdAt: Date
    let updatedAt: Date
    let resolvedAt: Date?
    let responseNote: String?

    enum CodingKeys: String, CodingKey {
        case id
        case chatId = "chat_id"
        case type, status
        case createdById = "created_by_id"
        case createdByName = "created_by_name"
        case targetUserId = "target_user_id"
        case title, description, amount, currency, attachments
        case expiresAt = "expires_at"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case resolvedAt = "resolved_at"
        case responseNote = "response_note"
    }

    func toChatRequest() -> ChatRequest {
        ChatRequest(
            id: id, chatId: chatId,
            type: RequestType(rawValue: type) ?? .generic,
            status: RequestStatus(rawValue: status) ?? .pending,
            createdById: createdById, createdByName: createdByName,
            targetUserId: targetUserId,
            title: title, description: description,
            amount: amount, currency: currency,
            attachments: (attachments ?? []).map { $0.toAttachment() },
            expiresAt: expiresAt, createdAt: createdAt, updatedAt: updatedAt,
            resolvedAt: resolvedAt, responseNote: responseNote
        )
    }
}

struct ChatRequestsListAPIResponse: Codable {
    let requests: [ChatRequestAPIResponse]
}

struct ChatDetailsAPIResponse: Codable {
    let chatId: UUID
    let car: ChatCarInfoAPIResponse
    let counterparty: ChatParticipantInfoAPIResponse
    let autoTranslationEnabled: Bool
    let notificationsMuted: Bool
    let documentsCount: Int
    let mediaCount: Int
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case chatId = "chat_id"
        case car, counterparty
        case autoTranslationEnabled = "auto_translation_enabled"
        case notificationsMuted = "notifications_muted"
        case documentsCount = "documents_count"
        case mediaCount = "media_count"
        case createdAt = "created_at"
    }

    func toChatDetails() -> ChatDetails {
        ChatDetails(
            chatId: chatId, car: car.toChatCarInfo(),
            counterparty: counterparty.toChatParticipantInfo(),
            autoTranslateEnabled: autoTranslationEnabled,
            notificationsMuted: notificationsMuted,
            documentsCount: documentsCount, mediaCount: mediaCount,
            createdAt: createdAt
        )
    }
}

struct ChatCarInfoAPIResponse: Codable {
    let id: UUID
    let title: String
    let coverPhotoUrl: String?
    let status: String
    let weeklyRentPrice: Double?
    let currency: String

    enum CodingKeys: String, CodingKey {
        case id, title
        case coverPhotoUrl = "cover_photo_url"
        case status
        case weeklyRentPrice = "weekly_rent_price"
        case currency
    }

    func toChatCarInfo() -> ChatCarInfo {
        ChatCarInfo(id: id, title: title, coverPhotoURL: coverPhotoUrl,
                    status: status, weeklyRentPrice: weeklyRentPrice, currency: currency)
    }
}

struct ChatParticipantInfoAPIResponse: Codable {
    let id: UUID
    let name: String
    let avatarUrl: String?
    let role: String
    let memberSince: Date

    enum CodingKeys: String, CodingKey {
        case id, name
        case avatarUrl = "avatar_url"
        case role
        case memberSince = "member_since"
    }

    func toChatParticipantInfo() -> ChatParticipantInfo {
        ChatParticipantInfo(id: id, name: name, avatarURL: avatarUrl,
                            role: role, memberSince: memberSince)
    }
}

struct CounterpartyProfileAPIResponse: Codable {
    let id: UUID
    let firstName: String
    let lastName: String
    let avatarUrl: String?
    let role: String
    let memberSince: Date
    let phone: String?
    let licenseDocumentUrl: String?
    let totalTrips: Int?
    let yearsLicensed: Int?
    let mechanicName: String?
    let mechanicPhone: String?
    let totalListings: Int?

    enum CodingKeys: String, CodingKey {
        case id
        case firstName = "first_name"
        case lastName = "last_name"
        case avatarUrl = "avatar_url"
        case role
        case memberSince = "member_since"
        case phone
        case licenseDocumentUrl = "license_document_url"
        case totalTrips = "total_trips"
        case yearsLicensed = "years_licensed"
        case mechanicName = "mechanic_name"
        case mechanicPhone = "mechanic_phone"
        case totalListings = "total_listings"
    }

    func toProfile() -> CounterpartyProfile {
        CounterpartyProfile(
            id: id, firstName: firstName, lastName: lastName,
            avatarURL: avatarUrl,
            role: UserRole(rawValue: role) ?? .driver,
            memberSince: memberSince, phone: phone,
            licenseDocumentURL: licenseDocumentUrl,
            totalTrips: totalTrips, yearsLicensed: yearsLicensed,
            mechanicName: mechanicName, mechanicPhone: mechanicPhone,
            totalListings: totalListings
        )
    }
}

struct AttachmentsListAPIResponse: Codable {
    let attachments: [ChatAttachmentAPIResponse]
}

// MARK: - Actions (Today tab)

struct ActionItemAPIResponse: Codable {
    let requestId: UUID
    let requestType: String
    let requestStatus: String
    let chatId: UUID
    let carId: UUID
    let carTitle: String
    let carCoverPhotoUrl: String?
    let createdById: UUID
    let createdByName: String
    let targetUserId: UUID
    let targetUserName: String
    let title: String
    let description: String
    let amount: Double?
    let currency: String
    let expiresAt: Date
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case requestId = "request_id"
        case requestType = "request_type"
        case requestStatus = "request_status"
        case chatId = "chat_id"
        case carId = "car_id"
        case carTitle = "car_title"
        case carCoverPhotoUrl = "car_cover_photo_url"
        case createdById = "created_by_id"
        case createdByName = "created_by_name"
        case targetUserId = "target_user_id"
        case targetUserName = "target_user_name"
        case title, description, amount, currency
        case expiresAt = "expires_at"
        case createdAt = "created_at"
    }

    func toOnboardingTask() -> OnboardingTask {
        let requestType = RequestType(rawValue: requestType) ?? .generic
        let options: [String]
        switch requestType {
        case .manualPayment, .additionalFee:
            options = ["Accept", "Decline"]
        case .mechanicService:
            options = ["Accept", "Decline"]
        case .delayedPayment:
            options = ["Accept", "Decline"]
        case .generic:
            options = ["Accept", "Decline"]
        }

        return OnboardingTask(
            id: requestId,
            title: title,
            description: description,
            dueDate: expiresAt,
            requestedBy: createdByName,
            priority: .high,
            options: options,
            selectedOptionIndex: nil,
            countdown: CountdownConfig(deadline: expiresAt),
            chatId: chatId,
            carTitle: carTitle,
            requestType: self.requestType,
            counterpartyId: createdById,
            counterpartyName: createdByName
        )
    }
}

struct ActionsListAPIResponse: Codable {
    let actions: [ActionItemAPIResponse]
}

// MARK: - API Request Models

struct SendMessageAPIRequest: Codable {
    let body: String
    let clientMessageId: UUID

    enum CodingKeys: String, CodingKey {
        case body
        case clientMessageId = "client_message_id"
    }
}

struct FindOrCreateChatAPIRequest: Codable {
    let carId: UUID
    let driverId: UUID
    let ownerId: UUID

    enum CodingKeys: String, CodingKey {
        case carId = "car_id"
        case driverId = "driver_id"
        case ownerId = "owner_id"
    }
}

struct CreateChatRequestAPIRequest: Codable {
    let type: String
    let title: String
    let description: String
    let amount: Double?
    let currency: String?
}

struct RespondToRequestAPIRequest: Codable {
    let action: String
    let note: String?
}

struct UpdateChatSettingsAPIRequest: Codable {
    let autoTranslationEnabled: Bool?
    let notificationsMuted: Bool?

    enum CodingKeys: String, CodingKey {
        case autoTranslationEnabled = "auto_translation_enabled"
        case notificationsMuted = "notifications_muted"
    }
}

struct ArchiveChatAPIRequest: Codable {
    let archived: Bool
}

// MARK: - Chat API Response (for FindOrCreate)

struct ChatAPIResponse: Codable {
    let id: UUID
    let carId: UUID
    let driverId: UUID
    let ownerId: UUID
    let lastMessageAt: Date?
    let lastRequestAt: Date?
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case carId = "car_id"
        case driverId = "driver_id"
        case ownerId = "owner_id"
        case lastMessageAt = "last_message_at"
        case lastRequestAt = "last_request_at"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }
}
