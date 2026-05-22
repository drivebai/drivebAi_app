import Foundation
import SwiftUI
import Combine

enum AccidentStep: Int, CaseIterable {
    case photos = 0
    case driver1
    case driver2
    case damage
    case description
    case insurance
    case other
    case signature
    case review

    var title: String {
        switch self {
        case .photos:      return "Photos & Videos"
        case .driver1:     return "Driver 1 Info"
        case .driver2:     return "Driver 2 Info"
        case .damage:      return "Vehicle Damage"
        case .description: return "Accident Description"
        case .insurance:   return "Insurance"
        case .other:       return "Other Info"
        case .signature:   return "Signature"
        case .review:      return "Review & Submit"
        }
    }
}

@MainActor
final class AccidentReportViewModel: ObservableObject {
    @Published var currentStep: AccidentStep = .photos
    @Published var accident: AccidentAPIResponse?
    @Published var isLoading = false
    @Published var isSaving = false
    @Published var isSubmitting = false
    @Published var error: String?
    @Published var isSubmitted = false

    // Step data (bound to form fields)
    @Published var driver1Info = DriverInfoAPI()
    @Published var driver2Info = DriverInfoAPI()
    @Published var hasSecondDriver = false
    @Published var vehicleDamage = VehicleDamageAPI()
    @Published var accidentDescription = ""
    @Published var insuranceInfo = InsuranceInfoAPI()
    @Published var otherInfo = OtherInfoAPI()
    @Published var signatureImageData: Data?

    @Published var isUploadingAttachment = false
    @Published var uploadProgress: Double = 0

    private let relatedChatId: UUID?
    private let relatedCarId: UUID?

    init(relatedChatId: UUID? = nil, relatedCarId: UUID? = nil) {
        self.relatedChatId = relatedChatId
        self.relatedCarId = relatedCarId
    }

    var accidentId: UUID? { accident?.id }

    // MARK: - Lifecycle

    func loadOrCreate() async {
        guard accident == nil else { return }
        isLoading = true
        error = nil
        do {
            let a = try await APIClient.shared.createAccident(
                relatedChatId: relatedChatId,
                relatedCarId: relatedCarId
            )
            accident = a
            populateFromAccident(a)
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }

    // MARK: - Navigation

    var canGoBack: Bool { currentStep.rawValue > 0 }
    var isLastStep: Bool { currentStep == .review }

    func goBack() {
        guard canGoBack else { return }
        currentStep = AccidentStep(rawValue: currentStep.rawValue - 1)!
    }

    func goForward() async {
        await saveCurrentStep()
        guard error == nil else { return }
        if isLastStep { return }
        currentStep = AccidentStep(rawValue: currentStep.rawValue + 1)!
    }

    // MARK: - Save step

    func saveCurrentStep() async {
        guard let accidentId else { return }
        isSaving = true
        error = nil

        var patch = AccidentPatchRequest()
        switch currentStep {
        case .driver1:
            patch.driver1Info = driver1Info
        case .driver2:
            if hasSecondDriver { patch.driver2Info = driver2Info }
        case .damage:
            patch.vehicleDamage = vehicleDamage
        case .description:
            patch.accidentDescription = accidentDescription
        case .insurance:
            patch.insuranceInfo = insuranceInfo
        case .other:
            patch.otherInfo = otherInfo
        default:
            isSaving = false
            return
        }

        do {
            let updated = try await APIClient.shared.patchAccident(id: accidentId, patch: patch)
            accident = updated
        } catch {
            self.error = "Failed to save: \(error.localizedDescription)"
        }
        isSaving = false
    }

    // MARK: - Upload attachment

    func uploadAttachment(slot: String, data: Data, filename: String, mimeType: String) async {
        guard let accidentId else { return }
        isUploadingAttachment = true
        error = nil
        do {
            let _ = try await APIClient.shared.uploadAccidentAttachment(
                accidentId: accidentId, slot: slot, data: data, filename: filename, mimeType: mimeType
            )
            // Refresh accident to get updated attachments list
            let updated = try await APIClient.shared.getAccident(id: accidentId)
            accident = updated
        } catch {
            self.error = "Upload failed: \(error.localizedDescription)"
        }
        isUploadingAttachment = false
    }

    func deleteAttachment(id: UUID) async {
        guard let accidentId else { return }
        do {
            try await APIClient.shared.deleteAccidentAttachment(accidentId: accidentId, attachmentId: id)
            let updated = try await APIClient.shared.getAccident(id: accidentId)
            accident = updated
        } catch {
            self.error = "Delete failed: \(error.localizedDescription)"
        }
    }

    // MARK: - Sign

    func uploadSignature(imageData: Data) async {
        guard let accidentId else { return }
        isSaving = true
        do {
            let _ = try await APIClient.shared.uploadAccidentSignature(accidentId: accidentId, imageData: imageData)
            let updated = try await APIClient.shared.getAccident(id: accidentId)
            accident = updated
            signatureImageData = imageData
        } catch {
            self.error = "Signature upload failed: \(error.localizedDescription)"
        }
        isSaving = false
    }

    // MARK: - Submit

    func submit() async {
        guard let accidentId else { return }
        isSubmitting = true
        error = nil
        do {
            let updated = try await APIClient.shared.submitAccident(id: accidentId)
            accident = updated
            isSubmitted = true
        } catch {
            self.error = "Submission failed: \(error.localizedDescription)"
        }
        isSubmitting = false
    }

    // MARK: - Private

    private func populateFromAccident(_ a: AccidentAPIResponse) {
        if let d1 = a.driver1Info { driver1Info = d1 }
        if let d2 = a.driver2Info { driver2Info = d2; hasSecondDriver = true }
        if let vd = a.vehicleDamage { vehicleDamage = vd }
        if let desc = a.accidentDescription { accidentDescription = desc }
        if let ins = a.insuranceInfo { insuranceInfo = ins }
        if let oth = a.otherInfo { otherInfo = oth }
    }
}
