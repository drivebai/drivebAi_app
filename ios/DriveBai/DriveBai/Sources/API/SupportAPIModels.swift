import Foundation

// MARK: - Support Chat API Responses

struct SupportChatAPIResponse: Codable {
    let id: UUID
    let userId: UUID
    let unreadCount: Int
    let adminLastReadAt: Date?   // "Seen by support" up to this time
    let lastMessageAt: Date?
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case userId = "user_id"
        case unreadCount = "unread_count"
        case adminLastReadAt = "admin_last_read_at"
        case lastMessageAt = "last_message_at"
        case createdAt = "created_at"
    }
}

struct SupportAttachmentAPI: Codable, Identifiable, Equatable {
    let id: UUID
    let fileUrl: String      // already signed by the server
    let fileSize: Int64
    let mimeType: String
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case fileUrl = "file_url"
        case fileSize = "file_size"
        case mimeType = "mime_type"
        case createdAt = "created_at"
    }

    var isImage: Bool { mimeType.hasPrefix("image/") }
}

struct SupportMessageAPIResponse: Codable, Identifiable {
    let id: UUID
    let supportChatId: UUID
    let senderId: UUID
    let senderKind: String   // "user" | "admin"
    let body: String
    let attachments: [SupportAttachmentAPI]
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case supportChatId = "support_chat_id"
        case senderId = "sender_id"
        case senderKind = "sender_kind"
        case body
        case attachments
        case createdAt = "created_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UUID.self, forKey: .id)
        supportChatId = try c.decode(UUID.self, forKey: .supportChatId)
        senderId = try c.decode(UUID.self, forKey: .senderId)
        senderKind = try c.decode(String.self, forKey: .senderKind)
        body = try c.decode(String.self, forKey: .body)
        // Older frames (and text-only messages) may omit attachments entirely.
        attachments = try c.decodeIfPresent([SupportAttachmentAPI].self, forKey: .attachments) ?? []
        createdAt = try c.decode(Date.self, forKey: .createdAt)
    }
}

struct SupportMessagesListAPIResponse: Codable {
    let messages: [SupportMessageAPIResponse]
}

// MARK: - Domain Models

struct SupportMessage: Identifiable, Equatable {
    let id: UUID
    let supportChatId: UUID
    let senderId: UUID
    let senderKind: SenderKind
    let body: String
    let attachments: [SupportAttachmentAPI]
    let createdAt: Date

    enum SenderKind: String {
        case user
        case admin
    }

    var isFromAdmin: Bool { senderKind == .admin }
    var hasText: Bool { !body.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
}

extension SupportMessageAPIResponse {
    func toSupportMessage() -> SupportMessage {
        SupportMessage(
            id: id,
            supportChatId: supportChatId,
            senderId: senderId,
            senderKind: SupportMessage.SenderKind(rawValue: senderKind) ?? .user,
            body: body,
            attachments: attachments,
            createdAt: createdAt
        )
    }
}

// MARK: - Request Bodies

struct SendSupportMessageRequest: Encodable {
    let body: String
}
