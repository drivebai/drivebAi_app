import Foundation

// MARK: - Request Type

enum RequestType: String, Codable, CaseIterable, Identifiable {
    case manualPayment = "manual_payment"
    case delayedPayment = "delayed_payment"
    case mechanicService = "mechanic_service"
    case additionalFee = "additional_fee"
    case generic = "generic"

    var id: String { rawValue }

    var displayTitle: String {
        switch self {
        case .manualPayment: return "Manual Payment"
        case .delayedPayment: return "Delayed Payment"
        case .mechanicService: return "Mechanic Service"
        case .additionalFee: return "Additional Fee"
        case .generic: return "Other Request"
        }
    }

    var iconName: String {
        switch self {
        case .manualPayment: return "banknote"
        case .delayedPayment: return "clock.arrow.circlepath"
        case .mechanicService: return "wrench.and.screwdriver"
        case .additionalFee: return "plus.circle"
        case .generic: return "doc.text"
        }
    }

    var allowedRoles: [UserRole] {
        switch self {
        case .manualPayment: return [.driver, .carOwner]
        case .delayedPayment: return [.driver]
        case .mechanicService: return [.driver, .carOwner]
        case .additionalFee: return [.carOwner]
        case .generic: return [.driver, .carOwner]
        }
    }
}

// MARK: - Request Status

enum RequestStatus: String, Codable {
    case pending
    case accepted
    case declined
    case expired
    case cancelled

    var displayText: String {
        rawValue.capitalized
    }

    var isTerminal: Bool {
        self != .pending
    }
}

// MARK: - Request Action

enum RequestAction: String, Codable {
    case accept
    case decline
    case cancel
}

// MARK: - Chat Request

struct ChatRequest: Identifiable, Equatable {
    let id: UUID
    let chatId: UUID
    let type: RequestType
    let status: RequestStatus
    let createdById: UUID
    let createdByName: String
    let targetUserId: UUID
    let title: String
    let description: String
    let amount: Double?
    let currency: String
    let attachments: [ChatAttachment]
    let expiresAt: Date
    let createdAt: Date
    let updatedAt: Date
    let resolvedAt: Date?
    let responseNote: String?

    var isExpired: Bool {
        Date() > expiresAt
    }

    var isActionable: Bool {
        status == .pending && !isExpired
    }

    var remainingTime: (days: Int, hours: Int, minutes: Int) {
        let interval = expiresAt.timeIntervalSince(Date())
        guard interval > 0 else { return (0, 0, 0) }
        let totalMinutes = Int(interval / 60)
        let days = totalMinutes / (60 * 24)
        let hours = (totalMinutes % (60 * 24)) / 60
        let minutes = totalMinutes % 60
        return (days, hours, minutes)
    }

    var formattedAmount: String? {
        guard let amount = amount else { return nil }
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = currency
        return formatter.string(from: NSNumber(value: amount))
    }
}

// MARK: - Create Request Input

struct CreateRequestInput {
    var type: RequestType = .generic
    var title: String = ""
    var description: String = ""
    var amount: Double? = nil
    var currency: String = "USD"
    var attachmentData: [(data: Data, filename: String, mimeType: String)] = []

    var isValid: Bool {
        switch type {
        case .manualPayment, .additionalFee:
            return !title.isEmpty && amount != nil && (amount ?? 0) > 0
        case .delayedPayment, .mechanicService:
            return !title.isEmpty
        case .generic:
            return !title.isEmpty && !description.isEmpty
        }
    }

    static func == (lhs: CreateRequestInput, rhs: CreateRequestInput) -> Bool {
        lhs.type == rhs.type && lhs.title == rhs.title && lhs.description == rhs.description && lhs.amount == rhs.amount
    }
}
