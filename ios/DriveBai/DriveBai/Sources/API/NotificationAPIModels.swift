import Foundation

// MARK: - API Response

struct NotificationAPIResponse: Codable {
    let id: UUID
    let type: String
    let title: String
    let body: String
    let relatedChatId: UUID?
    let relatedLeaseRequestId: UUID?
    let isRead: Bool
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id, type, title, body
        case relatedChatId = "related_chat_id"
        case relatedLeaseRequestId = "related_lease_request_id"
        case isRead = "is_read"
        case createdAt = "created_at"
    }

    func toNotificationItem() -> NotificationItem {
        let notifType: NotificationType
        switch type {
        case "payment":   notifType = .payment
        case "system":    notifType = .system
        default:          notifType = .booking
        }
        return NotificationItem(
            id: id,
            type: notifType,
            title: title,
            body: body,
            date: createdAt,
            isRead: isRead,
            relatedChatId: relatedChatId
        )
    }
}

struct NotificationsListAPIResponse: Codable {
    let notifications: [NotificationAPIResponse]
    let unreadCount: Int

    enum CodingKeys: String, CodingKey {
        case notifications
        case unreadCount = "unread_count"
    }
}

// MARK: - Device Token

struct RegisterDeviceTokenAPIRequest: Codable {
    let token: String
    let platform: String
    let sandbox: Bool
}
