import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

/// Buyer-side form when the vehicle inspection fails.  Requires a reason
/// category, a 20-2000 character explanation, and at least one piece of
/// evidence.  Each attachment uploads immediately so the "Submit" tap
/// only carries the evidence-id list, mirroring the accident report
/// pattern.
struct RejectionEvidenceFormView: View {
    let purchaseRequest: PurchaseRequest
    let onRejectionSubmitted: (PurchaseRequest) -> Void

    @Environment(\.dismiss) private var dismiss

    @State private var reason: PurchaseRejectionReason = .undisclosedDamage
    @State private var explanation: String = ""
    @State private var uploadedEvidence: [PurchaseRejectionEvidence] = []
    @State private var isSubmitting = false
    @State private var errorMessage: String?

    @State private var photoPickerItems: [PhotosPickerItem] = []
    @State private var isPickingPhotos = false
    @State private var showFileImporter = false
    @State private var uploadingCount = 0

    private static let minExplanation = 20
    private static let maxExplanation = 2000
    private static let maxEvidenceFiles = 20

    private var isValid: Bool {
        explanation.count >= Self.minExplanation
            && explanation.count <= Self.maxExplanation
            && !uploadedEvidence.isEmpty
    }

    var body: some View {
        Form {
            reasonSection
            explanationSection
            evidenceSection
            submitSection
        }
        .navigationTitle("Reject vehicle")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }
                    .disabled(isSubmitting)
            }
        }
        .alert("Something went wrong", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK") { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "")
        }
        .photosPicker(
            isPresented: $isPickingPhotos,
            selection: $photoPickerItems,
            maxSelectionCount: Self.maxEvidenceFiles,
            selectionBehavior: .ordered,
            matching: .any(of: [.images, .videos])
        )
        .onChange(of: photoPickerItems) { _, items in
            guard !items.isEmpty else { return }
            Task { await handlePickedPhotos(items) }
        }
        .fileImporter(
            isPresented: $showFileImporter,
            allowedContentTypes: [.pdf, .image, .data],
            allowsMultipleSelection: true
        ) { result in
            Task { await handlePickedFiles(result) }
        }
    }

    // MARK: - Sections

    private var reasonSection: some View {
        Section("Reason") {
            Picker("Category", selection: $reason) {
                ForEach(PurchaseRejectionReason.allCases) { r in
                    Text(r.displayText).tag(r)
                }
            }
            .pickerStyle(.menu)
        }
    }

    private var explanationSection: some View {
        Section {
            TextEditor(text: $explanation)
                .frame(minHeight: 130)
                .disabled(isSubmitting)
            Text("\(explanation.count)/\(Self.maxExplanation) — minimum \(Self.minExplanation)")
                .font(.caption)
                .foregroundColor(explanation.count < Self.minExplanation
                                 || explanation.count > Self.maxExplanation
                                 ? .red : .secondary)
                .frame(maxWidth: .infinity, alignment: .trailing)
        } header: {
            Text("What went wrong?")
        } footer: {
            Text("Be specific. DrivaBai support will review the evidence you attach below and decide whether to release the hold on your payment.")
        }
    }

    private var evidenceSection: some View {
        Section {
            if uploadedEvidence.isEmpty && uploadingCount == 0 {
                Text("Add at least one photo, video, or document showing the issue.")
                    .font(.footnote)
                    .foregroundColor(.secondary)
            } else {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 10) {
                        ForEach(uploadedEvidence) { evidence in
                            evidenceThumbnail(evidence)
                        }
                        ForEach(0..<uploadingCount, id: \.self) { _ in
                            uploadingPlaceholder
                        }
                    }
                }
            }

            HStack(spacing: 12) {
                Button {
                    isPickingPhotos = true
                } label: {
                    Label("Add photo / video", systemImage: "camera.fill")
                }
                .disabled(uploadedEvidence.count >= Self.maxEvidenceFiles || isSubmitting)

                Button {
                    showFileImporter = true
                } label: {
                    Label("Add document", systemImage: "doc.fill")
                }
                .disabled(uploadedEvidence.count >= Self.maxEvidenceFiles || isSubmitting)
            }
        } header: {
            Text("Evidence")
        } footer: {
            Text("Up to 20 files. Photos, videos, or PDFs, 50 MB each.")
        }
    }

    private var submitSection: some View {
        Section {
            Button(action: submit) {
                HStack {
                    if isSubmitting { ProgressView() }
                    Text(isSubmitting ? "Submitting…" : "Submit rejection")
                        .font(.headline)
                }
                .frame(maxWidth: .infinity)
            }
            .disabled(!isValid || isSubmitting)
        } footer: {
            Text("DrivaBai support typically responds within 24 hours. Your payment stays on hold until they resolve the case.")
        }
    }

    // MARK: - Evidence helpers

    private func evidenceThumbnail(_ evidence: PurchaseRejectionEvidence) -> some View {
        VStack(spacing: 4) {
            if evidence.isImage,
               let url = ImageURLHelper.fullURL(for: evidence.fileUrl) {
                RemoteImage(url: url, contentMode: .fill, maxPixelSize: 200)
                    .frame(width: 80, height: 80)
                    .clipped()
                    .cornerRadius(8)
            } else if evidence.isVideo {
                VStack(spacing: 4) {
                    Image(systemName: "play.rectangle.fill")
                        .font(.title2)
                        .foregroundColor(.driveBaiPrimary)
                    Text("Video")
                        .font(.caption2)
                }
                .frame(width: 80, height: 80)
                .background(Color(.systemGray6))
                .cornerRadius(8)
            } else {
                VStack(spacing: 4) {
                    Image(systemName: "doc.fill")
                        .font(.title2)
                        .foregroundColor(.driveBaiPrimary)
                    Text("Doc")
                        .font(.caption2)
                }
                .frame(width: 80, height: 80)
                .background(Color(.systemGray6))
                .cornerRadius(8)
            }
            Text(evidence.filename)
                .font(.caption2)
                .lineLimit(1)
                .frame(width: 80)
        }
    }

    private var uploadingPlaceholder: some View {
        VStack {
            ProgressView()
            Text("Uploading…").font(.caption2).foregroundColor(.secondary)
        }
        .frame(width: 80, height: 80)
        .background(Color(.systemGray6))
        .cornerRadius(8)
    }

    // MARK: - Upload handlers

    private func handlePickedPhotos(_ items: [PhotosPickerItem]) async {
        defer { photoPickerItems = [] }
        for item in items {
            guard let data = try? await item.loadTransferable(type: Data.self) else { continue }
            let utt = item.supportedContentTypes.first
            let mime = utt?.preferredMIMEType ?? "image/jpeg"
            let ext = utt?.preferredFilenameExtension ?? "jpg"
            let filename = "evidence-\(UUID().uuidString.prefix(8)).\(ext)"
            await uploadEvidence(data: data, filename: filename, mimeType: mime)
        }
    }

    private func handlePickedFiles(_ result: Result<[URL], Error>) async {
        switch result {
        case .failure(let err):
            errorMessage = err.localizedDescription
        case .success(let urls):
            for url in urls {
                let didStart = url.startAccessingSecurityScopedResource()
                defer { if didStart { url.stopAccessingSecurityScopedResource() } }
                guard let data = try? Data(contentsOf: url) else { continue }
                let mime = UTType(filenameExtension: url.pathExtension)?
                    .preferredMIMEType ?? "application/octet-stream"
                await uploadEvidence(
                    data: data,
                    filename: url.lastPathComponent,
                    mimeType: mime
                )
            }
        }
    }

    private func uploadEvidence(data: Data, filename: String, mimeType: String) async {
        uploadingCount += 1
        defer { uploadingCount = max(0, uploadingCount - 1) }
        do {
            let response = try await APIClient.shared.uploadRejectionEvidence(
                purchaseRequestId: purchaseRequest.id,
                fileData: data,
                filename: filename,
                mimeType: mimeType
            )
            uploadedEvidence.append(response.toDomain())
        } catch let apiError as APIError {
            errorMessage = apiError.errorDescription
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    // MARK: - Submit

    private func submit() {
        guard !isSubmitting, isValid else { return }
        isSubmitting = true
        Task {
            defer { isSubmitting = false }
            do {
                _ = try await APIClient.shared.buyerRejectVehicle(
                    purchaseRequestId: purchaseRequest.id,
                    reason: reason,
                    explanation: explanation,
                    evidenceIds: uploadedEvidence.map(\.id)
                )
                // Re-fetch the purchase to pick up the new status.
                if let updated = try? await APIClient.shared.fetchPurchaseRequest(
                    id: purchaseRequest.id
                ) {
                    onRejectionSubmitted(updated.toDomain())
                }
                dismiss()
            } catch let apiError as APIError {
                errorMessage = apiError.errorDescription
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }
}
