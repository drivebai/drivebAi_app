import SwiftUI

struct ChatBubbleView: View {
    let message: ChatMessage
    /// Invoked when the user taps a `.failed` (retriable) outgoing bubble.
    /// The re-send reuses the message's existing `clientMessageId`, which the
    /// send path dedupes server-side, so a double-commit lands as one row.
    var onRetry: ((ChatMessage) -> Void)? = nil

    @State private var previewAttachment: ChatAttachment?
    @State private var pdfAttachment: ChatAttachment?

    /// A failed *text* bubble can be re-sent (we still hold its body +
    /// clientMessageId). Failed attachment bubbles are not retriable here —
    /// the picked file bytes aren't retained after the optimistic append —
    /// so they keep the plain error glyph.
    private var isRetriable: Bool {
        if case .failed = message.status { return message.messageType == "text" }
        return false
    }

    var body: some View {
        if message.isSystem {
            systemMessageView
        } else if message.isAdmin {
            adminMessageView
        } else if let attachment = message.attachments.first {
            attachmentBubble(attachment)
                .fullScreenCover(item: $previewAttachment) { att in
                    ImageAttachmentViewerView(attachment: att)
                }
                .sheet(item: $pdfAttachment) { att in
                    PDFViewerView(attachment: att)
                }
        } else if message.messageType == "attachment" {
            // Backend FK on attachments.message_id is ON DELETE SET NULL, so an
            // "attachment" message can outlive its attachment row. Render a
            // muted placeholder instead of falling through to a plain text bubble.
            removedAttachmentView
        } else {
            messageBubble
        }
    }

    private var removedAttachmentView: some View {
        HStack {
            if message.direction == .sent { Spacer(minLength: 60) }
            Label("Attachment unavailable", systemImage: "paperclip.badge.ellipsis")
                .font(.subheadline)
                .foregroundColor(.secondary)
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .background(Color(.systemGray6))
                .clipShape(RoundedRectangle(cornerRadius: 18))
            if message.direction == .received { Spacer(minLength: 60) }
        }
        .padding(.vertical, 1)
    }

    private var systemMessageView: some View {
        HStack {
            Spacer()
            Text(message.body)
                .font(.caption)
                .foregroundColor(.secondary)
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                .background(Color(.systemGray6))
                .clipShape(Capsule())
            Spacer()
        }
        .padding(.vertical, 4)
    }

    private var adminMessageView: some View {
        HStack(alignment: .bottom, spacing: 8) {
            VStack(alignment: .leading, spacing: 4) {
                Text("ADMIN")
                    .font(.caption2.bold())
                    .foregroundColor(.white)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(Color.purple)
                    .clipShape(Capsule())

                Text(message.body)
                    .font(.body)
                    .foregroundColor(.primary)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(Color.purple.opacity(0.12))
                    .clipShape(RoundedRectangle(cornerRadius: 18))
                    .overlay(
                        RoundedRectangle(cornerRadius: 18)
                            .stroke(Color.purple.opacity(0.3), lineWidth: 1)
                    )

                Text(formatTime(message.createdAt))
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }
            Spacer(minLength: 60)
        }
        .padding(.vertical, 1)
    }

    private var messageBubble: some View {
        HStack(alignment: .bottom, spacing: 8) {
            if message.direction == .sent { Spacer(minLength: 60) }

            VStack(alignment: message.direction == .sent ? .trailing : .leading, spacing: 2) {
                Text(message.body)
                    .font(.body)
                    .foregroundColor(message.direction == .sent ? .white : .primary)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(
                        message.direction == .sent
                            ? Color.driveBaiPrimary
                            : Color(.systemGray5)
                    )
                    .clipShape(RoundedRectangle(cornerRadius: 18))

                HStack(spacing: 4) {
                    Text(formatTime(message.createdAt))
                        .font(.caption2)
                        .foregroundColor(.secondary)

                    if message.direction == .sent {
                        statusIcon
                    }
                }
            }

            if message.direction == .received { Spacer(minLength: 60) }
        }
        .padding(.vertical, 1)
    }

    @ViewBuilder
    private var statusIcon: some View {
        switch message.status {
        case .sending:
            Image(systemName: "clock")
                .font(.caption2)
                .foregroundColor(.secondary)
        case .sent:
            Image(systemName: "checkmark")
                .font(.caption2)
                .foregroundColor(.secondary)
        case .failed:
            if isRetriable, let onRetry {
                Button {
                    onRetry(message)
                } label: {
                    HStack(spacing: 3) {
                        Image(systemName: "arrow.clockwise.circle.fill")
                            .font(.caption2)
                        Text("Tap to retry")
                            .font(.caption2)
                    }
                    .foregroundColor(.red)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Message failed to send. Tap to retry.")
            } else {
                Image(systemName: "exclamationmark.circle")
                    .font(.caption2)
                    .foregroundColor(.red)
            }
        }
    }

    private func formatTime(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "h:mm a"
        return formatter.string(from: date)
    }

    // MARK: - Attachment bubble

    @ViewBuilder
    private func attachmentBubble(_ attachment: ChatAttachment) -> some View {
        HStack(alignment: .bottom, spacing: 8) {
            if message.direction == .sent { Spacer(minLength: 60) }

            VStack(alignment: message.direction == .sent ? .trailing : .leading, spacing: 4) {
                Group {
                    if attachment.kind == .image {
                        imageAttachment(attachment)
                    } else {
                        fileAttachment(attachment)
                    }
                }
                .overlay(uploadingOverlay)

                HStack(spacing: 4) {
                    Text(formatTime(message.createdAt))
                        .font(.caption2)
                        .foregroundColor(.secondary)
                    if message.direction == .sent { statusIcon }
                }
            }

            if message.direction == .received { Spacer(minLength: 60) }
        }
        .padding(.vertical, 1)
    }

    @ViewBuilder
    private var uploadingOverlay: some View {
        if message.isOptimistic {
            ZStack {
                Color.black.opacity(0.18)
                ProgressView().tint(.white)
            }
            .clipShape(RoundedRectangle(cornerRadius: 16))
        }
    }

    private func imageAttachment(_ attachment: ChatAttachment) -> some View {
        let url = attachment.fullFileURL
        return Group {
            if let url {
                // Use RemoteImage (URLSession.shared-backed, with os.Logger
                // diagnostics for HTTP status + errors) — same component every
                // other image surface in the app uses. AsyncImage silently fails
                // on attachment URLs against the Fly static file server, leaving
                // a permanent placeholder; see Sources/Views/Components/RemoteImage.swift.
                RemoteImage(url: url, contentMode: .fill, maxPixelSize: 700)
            } else {
                attachmentPlaceholder(systemName: "photo")
            }
        }
        .frame(width: 220, height: 220)
        .clipShape(RoundedRectangle(cornerRadius: 16))
        .contentShape(Rectangle())
        .onTapGesture { previewAttachment = attachment }
    }

    private func attachmentPlaceholder(systemName: String) -> some View {
        ZStack {
            Color(.systemGray5)
            Image(systemName: systemName)
                .font(.title)
                .foregroundColor(.secondary)
        }
    }

    private func fileAttachment(_ attachment: ChatAttachment) -> some View {
        HStack(spacing: 10) {
            ZStack {
                RoundedRectangle(cornerRadius: 8)
                    .fill(Color.driveBaiPrimary.opacity(0.12))
                    .frame(width: 40, height: 40)
                Image(systemName: iconForMime(attachment.mimeType))
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundColor(.driveBaiPrimary)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(attachment.filename)
                    .font(.subheadline.weight(.medium))
                    .foregroundColor(message.direction == .sent ? .white : .primary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(Self.formatFileSize(attachment.fileSize))
                    .font(.caption2)
                    .foregroundColor(message.direction == .sent ? .white.opacity(0.85) : .secondary)
            }
            .frame(maxWidth: 200, alignment: .leading)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(
            message.direction == .sent ? Color.driveBaiPrimary : Color(.systemGray5)
        )
        .clipShape(RoundedRectangle(cornerRadius: 16))
        .contentShape(Rectangle())
        .onTapGesture {
            // PDFs open inside the app (PDFKit + download/share). Other file
            // kinds keep the previous behavior — hand off to the system so
            // iOS picks the appropriate viewer / browser.
            if isPDF(attachment) {
                pdfAttachment = attachment
            } else if let url = attachment.fullFileURL {
                UIApplication.shared.open(url)
            }
        }
    }

    private func isPDF(_ attachment: ChatAttachment) -> Bool {
        attachment.mimeType.lowercased().contains("pdf")
            || attachment.filename.lowercased().hasSuffix(".pdf")
    }

    private func iconForMime(_ mime: String) -> String {
        if mime.contains("pdf")   { return "doc.richtext.fill" }
        if mime.hasPrefix("video/") { return "play.rectangle.fill" }
        if mime.contains("zip")   { return "doc.zipper" }
        return "doc.fill"
    }

    private static func formatFileSize(_ bytes: Int) -> String {
        if bytes < 1024 { return "\(bytes) B" }
        if bytes < 1024 * 1024 { return "\(bytes / 1024) KB" }
        return String(format: "%.1f MB", Double(bytes) / (1024 * 1024))
    }
}

// Image full-screen viewer lives in
// Sources/Views/Chat/AttachmentViewer/ImageAttachmentViewerView.swift.
