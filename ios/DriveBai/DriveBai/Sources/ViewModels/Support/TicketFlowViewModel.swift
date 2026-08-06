import Foundation
import SwiftUI

// One-screen "Report a Problem" form state (was a 4-step wizard). The
// server-side draft machinery is unchanged — a draft is still born on open
// because evidence uploads against the ticket id — but every field now lives
// on a single screen with one Submit.
@MainActor
final class TicketFlowViewModel: ObservableObject {
    @Published var ticket: TicketAPIResponse?
    @Published var isLoading = false
    @Published var isSaving = false
    @Published var isSubmitting = false
    @Published var isSubmitted = false
    @Published var error: String?

    // Form fields.
    @Published var category: TicketCategory?
    @Published var subject: String = ""
    @Published var descriptionText: String = ""
    @Published var isUploadingAttachment = false

    var ticketId: UUID? { ticket?.id }
    var attachments: [TicketAttachmentAPI] { ticket?.attachments ?? [] }

    /// The same minimum the server validates at submit: a category and a
    /// non-empty description. The "Other" detail line and evidence are optional.
    var canSubmit: Bool {
        category != nil && !descriptionText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    // MARK: - Lifecycle

    func loadOrCreate() async {
        guard ticket == nil else { return }
        isLoading = true
        error = nil
        do {
            let t: TicketAPIResponse
            do {
                // Resume an existing draft; create only on 404.
                t = try await APIClient.shared.getTicketDraft()
            } catch {
                t = try await APIClient.shared.createTicket()
            }
            ticket = t
            populate(from: t)
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }

    private func populate(from t: TicketAPIResponse) {
        category = t.categoryEnum
        subject = t.subject
        descriptionText = t.description
    }

    // MARK: - Save

    /// Persist the form into the draft. Called before submit and when the
    /// sheet closes, so an abandoned form comes back filled next time.
    func saveDraft() async {
        guard let ticketId, !isSubmitted else { return }
        isSaving = true
        error = nil
        var patch = TicketPatchRequest()
        patch.category = category?.rawValue
        patch.subject = subject
        patch.description = descriptionText
        do {
            ticket = try await APIClient.shared.patchTicket(id: ticketId, patch: patch)
        } catch {
            self.error = "Couldn't save: \(error.localizedDescription)"
        }
        isSaving = false
    }

    // MARK: - Attachments

    func uploadAttachment(data: Data, filename: String, mimeType: String) async {
        guard let ticketId else { return }
        isUploadingAttachment = true
        error = nil
        do {
            _ = try await APIClient.shared.uploadTicketAttachment(
                ticketId: ticketId, data: data, filename: filename, mimeType: mimeType
            )
            ticket = try await APIClient.shared.getTicket(id: ticketId)
        } catch {
            self.error = "Upload failed: \(error.localizedDescription)"
        }
        isUploadingAttachment = false
    }

    func deleteAttachment(id: UUID) async {
        guard let ticketId else { return }
        do {
            try await APIClient.shared.deleteTicketAttachment(ticketId: ticketId, attachmentId: id)
            ticket = try await APIClient.shared.getTicket(id: ticketId)
        } catch {
            self.error = "Couldn't remove: \(error.localizedDescription)"
        }
    }

    // MARK: - Submit

    func submit() async {
        guard let ticketId else { return }
        await saveDraft()
        guard error == nil else { return }
        isSubmitting = true
        error = nil
        do {
            ticket = try await APIClient.shared.submitTicket(id: ticketId)
            isSubmitted = true
        } catch {
            self.error = "Submission failed: \(error.localizedDescription)"
        }
        isSubmitting = false
    }
}
