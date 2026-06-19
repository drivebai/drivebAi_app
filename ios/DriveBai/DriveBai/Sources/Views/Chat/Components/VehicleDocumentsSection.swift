import SwiftUI
import QuickLook

/// Driver-facing section displayed in the chat Requests tab. Lists the
/// car/owner documents the listing owner uploaded (registration, insurance,
/// inspection, permit). Counterpart of `DriverDocumentsSection` — same row
/// styling so the two surfaces feel familiar regardless of which side of
/// the chat you're on.
struct VehicleDocumentsSection: View {
    let documents: [VehicleDocumentAPIResponse]

    @State private var previewURL: URL?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Image(systemName: "car.fill")
                    .foregroundColor(.driveBaiPrimary)
                Text("Vehicle Documents")
                    .font(.headline)
            }

            VStack(spacing: 8) {
                ForEach(documents) { doc in
                    VehicleDocumentRow(document: doc) { url in
                        previewURL = url
                    }
                }
            }
        }
        .padding()
        .background(
            RoundedRectangle(cornerRadius: 12)
                .fill(Color(.systemBackground))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .stroke(Color(.separator), lineWidth: 0.5)
        )
        .quickLookPreview($previewURL)
    }
}

private struct VehicleDocumentRow: View {
    let document: VehicleDocumentAPIResponse
    let onOpen: (URL) -> Void

    @State private var isLoading = false
    @State private var errorMessage: String?

    private var iconName: String {
        switch document.documentType {
        case "registration": return "doc.text.fill"
        case "insurance":    return "shield.lefthalf.filled"
        case "inspection":   return "wrench.and.screwdriver.fill"
        case "permit":       return "checkmark.seal.fill"
        default:             return "doc.fill"
        }
    }

    var body: some View {
        Button(action: open) {
            HStack(spacing: 12) {
                Image(systemName: iconName)
                    .font(.title3)
                    .foregroundColor(.driveBaiPrimary)
                    .frame(width: 28)

                VStack(alignment: .leading, spacing: 2) {
                    Text(document.displayName)
                        .font(.subheadline)
                        .fontWeight(.medium)
                        .foregroundColor(.primary)
                    if let errorMessage {
                        Text(errorMessage)
                            .font(.caption)
                            .foregroundColor(.red)
                    } else {
                        Text(document.fileName)
                            .font(.caption)
                            .foregroundColor(.secondary)
                            .lineLimit(1)
                    }
                }

                Spacer()

                if isLoading {
                    ProgressView()
                } else {
                    Image(systemName: "eye")
                        .foregroundColor(.secondary)
                }
            }
            .padding(10)
            .background(Color(.systemGray6))
            .cornerRadius(8)
        }
        .buttonStyle(.plain)
        .disabled(isLoading)
    }

    private func open() {
        guard !isLoading else { return }
        guard let url = ImageURLHelper.fullURL(for: document.fileUrl) else {
            errorMessage = "Invalid document URL"
            return
        }
        isLoading = true
        errorMessage = nil
        Task {
            defer { Task { @MainActor in isLoading = false } }
            do {
                // Same download/cache path the driver-docs section uses, so
                // a second tap is instant and the auth header (when available)
                // is attached for signed URLs.
                let local = try await AttachmentDownloadService.shared.download(
                    url: url,
                    suggestedFilename: "vehicle-doc-\(document.id.uuidString)-\(document.fileName)"
                )
                await MainActor.run { onOpen(local) }
            } catch {
                await MainActor.run { errorMessage = "Couldn't open: \(error.localizedDescription)" }
            }
        }
    }
}
