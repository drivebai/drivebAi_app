import SwiftUI
import PhotosUI

// MARK: - Document Upload Card

/// Reusable card for uploading a document with status display
struct DocumentUploadCard: View {
    let type: DocumentType
    let document: Document?
    let isUploading: Bool
    @Binding var selectedItem: PhotosPickerItem?
    let onDelete: () -> Void

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
                        PhotosPicker(selection: $selectedItem, matching: .images) {
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
                PhotosPicker(selection: $selectedItem, matching: .images) {
                    HStack {
                        Spacer()
                        VStack(spacing: 8) {
                            Image(systemName: "arrow.up.doc")
                                .font(.system(size: 32))
                                .foregroundColor(.driveBaiPrimary)
                            Text("Tap to upload")
                                .font(.subheadline)
                                .foregroundColor(.driveBaiPrimary)
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
    }

    private func statusColor(_ status: DocumentStatus) -> Color {
        switch status {
        case .uploaded:
            return .orange
        case .verified:
            return .green
        case .rejected:
            return .red
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
            selectedItem: .constant(nil),
            onDelete: {}
        )

        DocumentUploadCard(
            type: .registration,
            document: nil,
            isUploading: true,
            selectedItem: .constant(nil),
            onDelete: {}
        )
    }
    .padding()
}
