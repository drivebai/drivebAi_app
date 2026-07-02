import Foundation

// MARK: - Purchase Rejection Evidence

struct PurchaseRejectionEvidence: Identifiable, Equatable, Hashable {
    let id: UUID
    let purchaseRejectionId: UUID
    let fileUrl: String
    let filename: String
    let mimeType: String
    let sizeBytes: Int64
    let createdAt: Date

    var isImage: Bool { mimeType.hasPrefix("image/") }
    var isVideo: Bool { mimeType.hasPrefix("video/") }
    var isPDF: Bool { mimeType.contains("pdf") }

    var formattedSize: String {
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: sizeBytes)
    }
}

// MARK: - Purchase Rejection

struct PurchaseRejection: Identifiable, Equatable, Hashable {
    let id: UUID
    let purchaseRequestId: UUID
    let reasonCategory: PurchaseRejectionReason
    let explanation: String
    let status: PurchaseRejectionStatus
    let refundStatus: PurchaseRefundStatus?
    let adminNote: String?
    let resolvedBy: UUID?
    let resolvedAt: Date?
    let evidence: [PurchaseRejectionEvidence]
    let createdAt: Date
    let updatedAt: Date

    var statusDisplayText: String {
        switch status {
        case .submitted: return "Submitted for review"
        case .underReview: return "Under review"
        case .accepted: return "Accepted by support"
        case .upheld: return "Sale upheld by support"
        case .withdrawn: return "Withdrawn by buyer"
        }
    }
}
