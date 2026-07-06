import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

// MARK: - Document Upload Card

/// Reusable card for uploading a document with status display.
/// Sources: Take Photo (when a camera exists), Photo Library, Files — via
/// the shared `DocumentSourcePicker` (QA pt 1). Every screen that renders
/// this card (onboarding, signup, ProfileView's driver-docs sheet) gets the
/// camera path automatically.
///
/// Preview (QA pt 11): tapping the uploaded-file row opens a
/// `DocumentPreviewSheet`. The sheet shows, in order of preference:
///   1. `remoteFileURL` — a signed URL for the stored document, when the
///      caller has one (the user-documents list API doesn't return URLs
///      yet, so existing call sites simply don't pass it), else
///   2. the locally staged bytes from the most recent pick this session.
struct DocumentUploadCard: View {
    let type: DocumentType
    let document: Document?
    let isUploading: Bool
    let onFileSelected: (Data, String, String) -> Void // (data, filename, mimeType)
    let onDelete: () -> Void
    /// Optional signed URL for the already-uploaded document, used for
    /// tap-to-preview. Defaults to nil so existing call sites are unchanged.
    var remoteFileURL: String? = nil

    @State private var showSourceChooser = false
    @State private var showPreview = false
    @State private var lastPicked: PickedDocument?

    private var previewSource: DocumentPreviewSheet.Source? {
        if let remoteFileURL, let doc = document {
            return .remoteURL(remoteFileURL, filename: doc.fileName)
        }
        if let picked = lastPicked {
            return .localData(picked.data, filename: picked.filename, mimeType: picked.mimeType)
        }
        return nil
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // Header
            HStack {
                Image(systemName: type == .driversLicense ? "person.text.rectangle" : "car.fill")
                    .font(.title2)
                    .foregroundColor(.driveBaiPrimary)

                VStack(alignment: .leading, spacing: 2) {
                    Text(type.displayName)
                        .font(.headline)

                    if let doc = document {
                        HStack(spacing: 4) {
                            Circle()
                                .fill(statusColor(doc.status))
                                .frame(width: 8, height: 8)
                            Text(doc.status.displayName)
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    } else {
                        Text("Not uploaded")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }

                Spacer()

                if document != nil {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundColor(.green)
                        .font(.title2)
                }
            }

            // Content
            if isUploading {
                HStack {
                    Spacer()
                    VStack(spacing: 8) {
                        ProgressView()
                        Text("Uploading...")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                    Spacer()
                }
                .padding(.vertical, 16)
            } else if let doc = document {
                // Show uploaded document info
                VStack(alignment: .leading, spacing: 8) {
                    Button {
                        if previewSource != nil { showPreview = true }
                    } label: {
                        HStack {
                            Image(systemName: "doc.fill")
                                .foregroundColor(.driveBaiPrimary)
                            Text(doc.fileName)
                                .font(.subheadline)
                                .foregroundColor(.primary)
                                .lineLimit(1)
                            Spacer()
                            Text(formatFileSize(doc.fileSize))
                                .font(.caption)
                                .foregroundColor(.secondary)
                            if previewSource != nil {
                                Image(systemName: "eye")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                            }
                        }
                        .padding(12)
                        .background(Color(.systemGray6))
                        .cornerRadius(8)
                    }
                    .buttonStyle(.plain)
                    .disabled(previewSource == nil)

                    // Action buttons
                    HStack(spacing: 12) {
                        Button(action: { showSourceChooser = true }) {
                            Label("Replace", systemImage: "arrow.triangle.2.circlepath")
                                .font(.subheadline)
                        }
                        .buttonStyle(.bordered)
                        .tint(.driveBaiPrimary)

                        Button(action: onDelete) {
                            Label("Remove", systemImage: "trash")
                                .font(.subheadline)
                        }
                        .buttonStyle(.bordered)
                        .tint(.red)
                    }
                }
            } else {
                // Upload prompt
                Button(action: { showSourceChooser = true }) {
                    HStack {
                        Spacer()
                        VStack(spacing: 8) {
                            Image(systemName: "arrow.up.doc")
                                .font(.system(size: 32))
                                .foregroundColor(.driveBaiPrimary)
                            Text("Tap to upload")
                                .font(.subheadline)
                                .foregroundColor(.driveBaiPrimary)
                            Text(CameraCaptureView.isCameraAvailable
                                 ? "Camera, Photo Library or Files"
                                 : "Photo Library or Files")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                        Spacer()
                    }
                    .padding(.vertical, 24)
                    .background(
                        RoundedRectangle(cornerRadius: 8)
                            .strokeBorder(style: StrokeStyle(lineWidth: 2, dash: [8]))
                            .foregroundColor(.driveBaiPrimary.opacity(0.5))
                    )
                }
            }
        }
        .padding()
        .background(Color(.systemBackground))
        .cornerRadius(12)
        .shadow(color: .black.opacity(0.05), radius: 8, x: 0, y: 2)
        .documentSourcePicker(isPresented: $showSourceChooser, filenameBase: type.rawValue) { picked in
            lastPicked = picked
            onFileSelected(picked.data, picked.filename, picked.mimeType)
        }
        .sheet(isPresented: $showPreview) {
            if let source = previewSource {
                DocumentPreviewSheet(source: source)
            }
        }
    }

    private func statusColor(_ status: DocumentStatus) -> Color {
        switch status {
        case .uploaded: return .orange
        case .verified: return .green
        case .rejected: return .red
        }
    }

    private func formatFileSize(_ bytes: Int64) -> String {
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return formatter.string(fromByteCount: bytes)
    }
}

#Preview {
    VStack(spacing: 16) {
        DocumentUploadCard(
            type: .driversLicense,
            document: nil,
            isUploading: false,
            onFileSelected: { _, _, _ in },
            onDelete: {}
        )

        DocumentUploadCard(
            type: .registration,
            document: nil,
            isUploading: true,
            onFileSelected: { _, _, _ in },
            onDelete: {}
        )
    }
    .padding()
}
