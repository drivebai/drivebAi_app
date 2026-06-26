import SwiftUI
import QuickLook

/// Owner-facing section displayed in the chat Requests tab when the driver has
/// shared their onboarding documents via a lease request. Each row opens a
/// QuickLook preview backed by a local cache of the downloaded file — images
/// and PDFs render inline without leaving the app.
struct DriverDocumentsSection: View {
    let documents: [SharedDocumentAPIResponse]

    @State private var previewURL: URL?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Image(systemName: "person.badge.shield.checkmark")
                    .foregroundColor(.driveBaiPrimary)
                Text("Driver Documents")
                    .font(.headline)
            }

            VStack(spacing: 8) {
                ForEach(documents) { doc in
                    DriverDocumentRow(document: doc) { url in
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

private struct DriverDocumentRow: View {
    let document: SharedDocumentAPIResponse
    let onOpen: (URL) -> Void

    @State private var isLoading = false
    @State private var errorMessage: String?

    private var displayName: String {
        document.documentType?.displayName ?? document.fileName
    }

    private var iconName: String {
        switch document.documentType {
        case .driversLicense:    return "person.text.rectangle"
        case .registration:      return "car.fill"
        case .commercialLicense: return "briefcase.fill"
        case .tlcLicense:        return "creditcard.fill"
        case .other:             return "doc.fill"
        case .none:              return "doc.fill"
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
                    Text(displayName)
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
                // Reuse the shared AttachmentDownloadService — it adds Bearer
                // auth if available AND short-circuits on cache hit, so the
                // second tap is instant. The filename gets sanitized and the
                // file lives in Caches/ChatAttachments/.
                let local = try await AttachmentDownloadService.shared.download(
                    url: url,
                    suggestedFilename: "shared-docs-\(document.id.uuidString)-\(document.fileName)"
                )
                await MainActor.run { onOpen(local) }
            } catch {
                await MainActor.run { errorMessage = "Couldn't open: \(error.localizedDescription)" }
            }
        }
    }
}
