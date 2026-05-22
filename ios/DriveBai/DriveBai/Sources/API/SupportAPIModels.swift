import Foundation

// MARK: - Support Chat API Responses

struct SupportChatAPIResponse: Codable {
    let id: UUID
    let userId: UUID
    let unreadCount: Int
    let lastMessageAt: Date?
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case userId = "user_id"
        case unreadCount = "unread_count"
        case lastMessageAt = "last_message_at"
        case createdAt = "created_at"
    }
}

struct SupportMessageAPIResponse: Codable, Identifiable {
    let id: UUID
    let supportChatId: UUID
    let senderId: UUID
    let senderKind: String   // "user" | "admin"
    let body: String
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case supportChatId = "support_chat_id"
        case senderId = "sender_id"
        case senderKind = "sender_kind"
        case body
        case createdAt = "created_at"
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
    let createdAt: Date

    enum SenderKind: String {
        case user
        case admin
    }

    var isFromAdmin: Bool { senderKind == .admin }
}

extension SupportMessageAPIResponse {
    func toSupportMessage() -> SupportMessage {
        SupportMessage(
            id: id,
            supportChatId: supportChatId,
            senderId: senderId,
            senderKind: SupportMessage.SenderKind(rawValue: senderKind) ?? .user,
            body: body,
            createdAt: createdAt
        )
    }
}

// MARK: - Request Bodies

struct SendSupportMessageRequest: Encodable {
    let body: String
}
