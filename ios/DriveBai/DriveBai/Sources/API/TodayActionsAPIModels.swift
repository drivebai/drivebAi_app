import Foundation

// MARK: - Today Actions API Models

struct TodayActionAPIModel: Codable {
    let id: UUID
    let type: String
    let title: String
    let body: String
    let carId: UUID
    let carTitle: String
    let chatId: UUID
    let counterpartyId: UUID
    let counterpartyName: String
    let status: String
    let createdAt: Date
    let expiresAt: Date
    let primaryAction: String
    let secondaryAction: String

    enum CodingKeys: String, CodingKey {
        case id, type, title, body, status
        case carId = "car_id"
        case carTitle = "car_title"
        case chatId = "chat_id"
        case counterpartyId = "counterparty_id"
        case counterpartyName = "counterparty_name"
        case createdAt = "created_at"
        case expiresAt = "expires_at"
        case primaryAction = "primary_action"
        case secondaryAction = "secondary_action"
    }

    func toOnboardingTask() -> OnboardingTask {
        OnboardingTask(
            id: id,
            title: title,
            description: body,
            dueDate: expiresAt,
            requestedBy: counterpartyName,
            priority: .high,
            options: [primaryAction.capitalized, secondaryAction.capitalized],
            countdown: CountdownConfig(deadline: expiresAt),
            chatId: chatId,
            carTitle: carTitle,
            requestType: type,
            counterpartyId: counterpartyId,
            counterpartyName: counterpartyName
        )
    }
}

struct TodayActionsAPIResponse: Codable {
    let actions: [TodayActionAPIModel]
    let hasUnreadActions: Bool

    enum CodingKeys: String, CodingKey {
        case actions
        case hasUnreadActions = "has_unread_actions"
    }
}
