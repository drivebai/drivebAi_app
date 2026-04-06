import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

// MARK: - Document Upload Card

/// Reusable card for uploading a document with status display.
/// Supports both Photo Library and Files as upload sources.
struct DocumentUploadCard: View {
    let type: DocumentType
    let document: Document?
    let isUploading: Bool
    let onFileSelected: (Data, String, String) -> Void // (data, filename, mimeType)
    let onDelete: () -> Void

    @State private var showSourceChooser = false
    @State private var showPhotoPicker = false
    @State private var showFilePicker = false
    @State private var pickerItem: PhotosPickerItem?

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
                    HStack {
                        Image(systemName: "doc.fill")
                            .foregroundColor(.driveBaiPrimary)
                        Text(doc.fileName)
                            .font(.subheadline)
                            .lineLimit(1)
                        Spacer()
                        Text(formatFileSize(doc.fileSize))
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                    .padding(12)
                    .background(Color(.systemGray6))
                    .cornerRadius(8)

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
                            Text("Photo Library or Files")
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
        .confirmationDialog("Upload Document", isPresented: $showSourceChooser, titleVisibility: .visible) {
            Button("Photo Library") { showPhotoPicker = true }
            Button("Files") { showFilePicker = true }
            Button("Cancel", role: .cancel) {}
        }
        .photosPicker(isPresented: $showPhotoPicker, selection: $pickerItem, matching: .images)
        .onChange(of: pickerItem) { _, newItem in
            guard let item = newItem else { return }
            pickerItem = nil
            Task {
                guard let data = try? await item.loadTransferable(type: Data.self) else { return }
                let mimeType: String
                let ext: String
                if let contentType = item.supportedContentTypes.first, contentType.conforms(to: .png) {
                    mimeType = "image/png"
                    ext = "png"
                } else {
                    mimeType = "image/jpeg"
                    ext = "jpg"
                }
                let filename = "\(type.rawValue).\(ext)"
                await MainActor.run { onFileSelected(data, filename, mimeType) }
            }
        }
        .fileImporter(
            isPresented: $showFilePicker,
            allowedContentTypes: [.pdf, .jpeg, .png, .image],
            allowsMultipleSelection: false
        ) { result in
            guard case .success(let urls) = result, let url = urls.first else { return }
            guard url.startAccessingSecurityScopedResource() else { return }
            defer { url.stopAccessingSecurityScopedResource() }
            guard let data = try? Data(contentsOf: url) else { return }
            let filename = url.lastPathComponent
            let mimeType = mimeTypeForURL(url)
            onFileSelected(data, filename, mimeType)
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

    private func mimeTypeForURL(_ url: URL) -> String {
        switch url.pathExtension.lowercased() {
        case "pdf": return "application/pdf"
        case "png": return "image/png"
        case "jpg", "jpeg": return "image/jpeg"
        default: return "application/octet-stream"
        }
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
