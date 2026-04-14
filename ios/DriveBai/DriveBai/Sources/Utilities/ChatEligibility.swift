import Foundation

/// Single source of truth for whether a "start chat / message" CTA
/// should be shown in the UI.
///
/// The rule is intentionally strict:
/// - Hide the CTA if either id is missing.
/// - Hide the CTA if the other party is the current user (no self-chat).
///
/// Context-specific suppression (e.g. hiding chat in management/edit
/// flows) is the caller's responsibility — this helper only answers the
/// id-level question.
enum ChatEligibility {
    static func canStartChat(currentUserId: UUID?, otherUserId: UUID?) -> Bool {
        guard let current = currentUserId, let other = otherUserId else { return false }
        return current != other
    }
}
