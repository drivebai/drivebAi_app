import Foundation
import SwiftUI

enum TicketStep: Int, CaseIterable {
    case category = 0
    case describe
    case attach
    case review

    var title: String {
        switch self {
        case .category: return "What do you need help with?"
        case .describe: return "Describe the problem"
        case .attach:   return "Add evidence"
        case .review:   return "Review & submit"
        }
    }
}

@MainActor
final class TicketFlowViewModel: ObservableObject {
    @Published var currentStep: TicketStep = .category
    @Published var ticket: TicketAPIResponse?
    @Published var isLoading = false
    @Published var isSaving = false
    @Published var isSubmitting = false
    @Published var isSubmitted = false
    @Published var error: String?

    // Step data bound to the form.
    @Published var category: TicketCategory?
    @Published var subject: String = ""
    @Published var descriptionText: String = ""
    @Published var isUploadingAttachment = false

    var ticketId: UUID? { ticket?.id }
    var attachments: [TicketAttachmentAPI] { ticket?.attachments ?? [] }

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
        // Resume where the user left off (accidents lost this — restarted at 0).
        if let step = TicketStep(rawValue: t.lastStep) {
            currentStep = step
        }
    }

    // MARK: - Navigation

    var canGoBack: Bool { currentStep.rawValue > 0 }
    var isLastStep: Bool { currentStep == .review }

    /// Whether the current step is complete enough to advance.
    var canAdvance: Bool {
        switch currentStep {
        case .category: return category != nil
        case .describe: return !descriptionText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        default:        return true
        }
    }

    func goBack() {
        guard canGoBack else { return }
        currentStep = TicketStep(rawValue: currentStep.rawValue - 1)!
    }

    func goForward() async {
        let next = min(currentStep.rawValue + 1, TicketStep.review.rawValue)
        await saveCurrentStep(lastStep: next)
        guard error == nil else { return }
        if isLastStep { return }
        currentStep = TicketStep(rawValue: next)!
    }

    // MARK: - Save

    private func saveCurrentStep(lastStep: Int) async {
        guard let ticketId else { return }
        isSaving = true
        error = nil
        var patch = TicketPatchRequest()
        patch.lastStep = lastStep
        switch currentStep {
        case .category:
            patch.category = category?.rawValue
        case .describe:
            patch.subject = subject
            patch.description = descriptionText
        default:
            break
        }
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
        // Persist the review step's cursor before submitting.
        await saveCurrentStep(lastStep: TicketStep.review.rawValue)
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
