import Foundation
import SwiftUI

// MARK: - Support Ticket API Models

/// The six ticket categories — a SERVER-SIDE enum (the backend stores the raw
/// value and the admin queue triages by it). The label/icon/blurb are the
/// iOS presentation of each case.
enum TicketCategory: String, CaseIterable, Identifiable {
    case account
    case listing
    case renting
    case buySell = "buy_sell"
    case payments
    case other

    var id: String { rawValue }

    var label: String {
        switch self {
        case .account:  return "Account & verification"
        case .listing:  return "Listing a car"
        case .renting:  return "Renting a car"
        case .buySell:  return "Buying or selling a car"
        case .payments: return "Payments & refunds"
        case .other:    return "Report a problem"
        }
    }

    var icon: String {
        switch self {
        case .account:  return "person.crop.circle.badge.questionmark"
        case .listing:  return "car.fill"
        case .renting:  return "key.fill"
        case .buySell:  return "doc.text.fill"
        case .payments: return "creditcard.fill"
        case .other:    return "exclamationmark.bubble.fill"
        }
    }

    var blurb: String {
        switch self {
        case .account:  return "Sign-up, verification, switching mode, documents"
        case .listing:  return "Creating or editing a listing, photos, VIN"
        case .renting:  return "Booking, pickup, handover, returns"
        case .buySell:  return "Offers, Bill of Sale, title, inspection"
        case .payments: return "A charge, a stuck payment, a refund"
        case .other:    return "A suspicious listing, a bad actor, anything else"
        }
    }
}

/// Ticket lifecycle. draft → open → resolved → closed (reopen from resolved).
enum TicketStatus: String {
    case draft
    case open
    case resolved
    case closed
    case unknown

    init(raw: String) { self = TicketStatus(rawValue: raw) ?? .unknown }

    var label: String {
        switch self {
        case .draft:    return "Draft"
        case .open:     return "Open"
        case .resolved: return "Resolved"
        case .closed:   return "Closed"
        case .unknown:  return "—"
        }
    }

    var color: Color {
        switch self {
        case .draft:    return .secondary
        case .open:     return .orange
        case .resolved: return .green
        case .closed:   return .secondary
        case .unknown:  return .secondary
        }
    }
}

struct TicketAttachmentAPI: Codable, Identifiable {
    let id: UUID
    let ticketId: UUID
    let fileUrl: String
    let fileSize: Int64
    let mimeType: String
    let createdAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case ticketId = "ticket_id"
        case fileUrl = "file_url"
        case fileSize = "file_size"
        case mimeType = "mime_type"
        case createdAt = "created_at"
    }
}

struct TicketAPIResponse: Codable, Identifiable {
    let id: UUID
    let userId: UUID
    let category: String
    let subject: String
    let description: String
    let status: String
    let lastStep: Int
    let submittedAt: Date?
    let resolvedAt: Date?
    let closedAt: Date?
    let attachments: [TicketAttachmentAPI]
    let createdAt: Date
    let updatedAt: Date

    enum CodingKeys: String, CodingKey {
        case id
        case userId = "user_id"
        case category, subject, description, status
        case lastStep = "last_step"
        case submittedAt = "submitted_at"
        case resolvedAt = "resolved_at"
        case closedAt = "closed_at"
        case attachments
        case createdAt = "created_at"
        case updatedAt = "updated_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UUID.self, forKey: .id)
        userId = try c.decode(UUID.self, forKey: .userId)
        category = try c.decodeIfPresent(String.self, forKey: .category) ?? ""
        subject = try c.decodeIfPresent(String.self, forKey: .subject) ?? ""
        description = try c.decodeIfPresent(String.self, forKey: .description) ?? ""
        status = try c.decode(String.self, forKey: .status)
        lastStep = try c.decodeIfPresent(Int.self, forKey: .lastStep) ?? 0
        submittedAt = try c.decodeIfPresent(Date.self, forKey: .submittedAt)
        resolvedAt = try c.decodeIfPresent(Date.self, forKey: .resolvedAt)
        closedAt = try c.decodeIfPresent(Date.self, forKey: .closedAt)
        attachments = try c.decodeIfPresent([TicketAttachmentAPI].self, forKey: .attachments) ?? []
        createdAt = try c.decode(Date.self, forKey: .createdAt)
        updatedAt = try c.decode(Date.self, forKey: .updatedAt)
    }

    var categoryEnum: TicketCategory? { TicketCategory(rawValue: category) }
    var statusEnum: TicketStatus { TicketStatus(raw: status) }
}

struct TicketListResponse: Codable {
    let tickets: [TicketAPIResponse]
}

/// Partial patch for a draft ticket. Only non-nil fields are sent.
struct TicketPatchRequest: Codable {
    var category: String?
    var subject: String?
    var description: String?
    var lastStep: Int?

    enum CodingKeys: String, CodingKey {
        case category, subject, description
        case lastStep = "last_step"
    }
}
