import Foundation

// MARK: - Chat Summary (for list screen)

struct ChatSummary: Identifiable, Equatable, Hashable {
    let id: UUID
    let carId: UUID
    let carTitle: String
    let carCoverPhotoURL: String?
    let counterpartyId: UUID
    let counterpartyName: String
    let counterpartyAvatarURL: String?
    let lastMessage: String?
    let lastMessageAt: Date?
    let unreadCount: Int
    let openRequestsCount: Int
    let isArchived: Bool

    var carCoverFullURL: URL? {
        ImageURLHelper.fullURL(for: carCoverPhotoURL)
    }

    var counterpartyAvatarFullURL: URL? {
        ImageURLHelper.fullURL(for: counterpartyAvatarURL)
    }
}

// MARK: - Chat Message

enum MessageDirection: Equatable {
    case sent
    case received
}

enum MessageStatus: Equatable {
    case sending
    case sent
    case failed(String)
}

struct ChatMessage: Identifiable, Equatable {
    let id: UUID
    let clientMessageId: UUID
    let chatId: UUID
    let senderId: UUID
    let senderName: String
    let senderKind: String // "user" | "admin"
    let direction: MessageDirection
    let messageType: String // "text" | "system" | "attachment"
    let body: String
    let attachments: [ChatAttachment]
    let createdAt: Date
    var status: MessageStatus

    var isOptimistic: Bool {
        if case .sending = status { return true }
        return false
    }

    var isSystem: Bool {
        messageType == "system"
    }

    var isAdmin: Bool {
        senderKind == "admin"
    }
}

// MARK: - Chat Attachment

enum AttachmentType: String, Codable {
    case image
    case document
    case video
}

struct ChatAttachment: Identifiable, Equatable {
    let id: UUID
    let kind: AttachmentType
    let filename: String
    let fileURL: String
    let fileSize: Int
    let mimeType: String

    var fullFileURL: URL? {
        ImageURLHelper.fullURL(for: fileURL)
    }
}

// MARK: - Chat Details

struct ChatDetails: Equatable {
    let chatId: UUID
    let car: ChatCarInfo
    let counterparty: ChatParticipantInfo
    let autoTranslateEnabled: Bool
    let notificationsMuted: Bool
    let documentsCount: Int
    let mediaCount: Int
    let createdAt: Date
}

struct ChatCarInfo: Equatable {
    let id: UUID
    let title: String
    let coverPhotoURL: String?
    let status: String
    let weeklyRentPrice: Double?
    let currency: String
}

struct ChatParticipantInfo: Equatable {
    let id: UUID
    let name: String
    let avatarURL: String?
    let role: String
    let memberSince: Date
}

// MARK: - Counterparty Profile

struct CounterpartyProfile: Equatable {
    let id: UUID
    let firstName: String
    let lastName: String
    let avatarURL: String?
    let role: UserRole
    let memberSince: Date
    let phone: String?
    // Driver fields (visible to owners)
    let licenseDocumentURL: String?
    let totalTrips: Int?
    let yearsLicensed: Int?
    // Owner fields (visible to drivers)
    let mechanicName: String?
    let mechanicPhone: String?
    let totalListings: Int?

    var fullName: String { "\(firstName) \(lastName)" }
}

// MARK: - Cursor-based Pagination

struct MessagePage {
    let messages: [ChatMessage]
    let nextCursor: String?
    let hasMore: Bool
}
