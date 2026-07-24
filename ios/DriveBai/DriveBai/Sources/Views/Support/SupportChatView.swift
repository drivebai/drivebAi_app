import SwiftUI

struct SupportChatView: View {
    @StateObject private var viewModel = SupportChatViewModel()
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @Environment(\.dismiss) private var dismiss
    @State private var showAttachPicker = false

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if viewModel.isLoading {
                    Spacer()
                    ProgressView()
                    Spacer()
                } else {
                    messagesArea
                    Divider()
                    composer
                }
            }
            .navigationTitle("Help & Support")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Close") { dismiss() }
                }
            }
            .alert("Error", isPresented: Binding(
                get: { viewModel.error != nil },
                set: { if !$0 { _ = viewModel.error } }
            )) {
                Button("OK", role: .cancel) {}
            } message: {
                Text(viewModel.error ?? "")
            }
        }
        .task {
            supportInboxStore.isSupportChatVisible = true
            await viewModel.loadOrCreate()
            await supportInboxStore.markRead()
        }
        .onDisappear {
            supportInboxStore.isSupportChatVisible = false
            Task { await viewModel.markRead() }
        }
    }

    // MARK: - Messages

    private var messagesArea: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 0) {
                    if viewModel.messages.isEmpty {
                        emptyState
                    } else {
                        ForEach(viewModel.messages) { msg in
                            SupportBubbleView(message: msg)
                                .padding(.horizontal, 16)
                                .padding(.vertical, 4)
                                .id(msg.id)
                            if msg.id == viewModel.seenIndicatorMessageID {
                                Text("Seen by support")
                                    .font(.caption2)
                                    .foregroundColor(.secondary)
                                    .frame(maxWidth: .infinity, alignment: .trailing)
                                    .padding(.horizontal, 16)
                                    .padding(.bottom, 2)
                            }
                        }
                        Color.clear.frame(height: 4).id("bottom")
                    }
                }
                .padding(.vertical, 12)
            }
            .onChange(of: viewModel.messages.count) { _ in
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo("bottom", anchor: .bottom)
                }
            }
            .onAppear {
                proxy.scrollTo("bottom", anchor: .bottom)
            }
        }
    }

    // MARK: - Empty State

    private var emptyState: some View {
        VStack(spacing: 20) {
            Spacer(minLength: 40)
            Image(systemName: "bubble.left.and.bubble.right.fill")
                .font(.system(size: 52))
                .foregroundColor(.driveBaiPrimary.opacity(0.7))

            Text("We're here to help")
                .font(.title2.bold())
                .foregroundColor(.primary)

            Text("Tell us what happened. We're a small team and read every message — we'll reply as soon as we can.")
                .font(.subheadline)
                .foregroundColor(.secondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)

            VStack(alignment: .leading, spacing: 10) {
                SupportTipRow(icon: "car.fill",       text: "Issues with a rental or booking")
                SupportTipRow(icon: "creditcard.fill", text: "Payment questions")
                SupportTipRow(icon: "doc.text.fill",  text: "Document verification help")
                SupportTipRow(icon: "questionmark.circle.fill", text: "Anything else")
            }
            .padding(.horizontal, 32)
            .padding(.top, 8)

            Spacer(minLength: 40)
        }
    }

    // MARK: - Composer

    private var composer: some View {
        HStack(alignment: .bottom, spacing: 10) {
            // Attach: camera, photo library, or a document. HEIC→JPEG transcode
            // is handled inside the picker. A caption typed in the field rides
            // along with the file.
            Button { showAttachPicker = true } label: {
                Group {
                    if viewModel.isUploadingAttachment {
                        ProgressView()
                    } else {
                        Image(systemName: "paperclip").font(.system(size: 22))
                    }
                }
                .frame(width: 34, height: 34)
                .foregroundColor(.driveBaiPrimary)
            }
            .disabled(viewModel.isUploadingAttachment)
            .documentSourcePicker(isPresented: $showAttachPicker, filenameBase: "support") { picked in
                Task { await viewModel.uploadAttachment(data: picked.data, filename: picked.filename, mimeType: picked.mimeType) }
            }

            TextField("Type a message…", text: $viewModel.draft, axis: .vertical)
                .lineLimit(1...5)
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .background(Color(.systemGray6))
                .clipShape(RoundedRectangle(cornerRadius: 22))

            Button {
                Task { await viewModel.sendMessage() }
            } label: {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.system(size: 34))
                    .foregroundColor(
                        viewModel.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                            ? .secondary.opacity(0.4)
                            : .driveBaiPrimary
                    )
            }
            .disabled(
                viewModel.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                || viewModel.isSending
            )
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(Color(.systemBackground))
    }
}

// MARK: - Support Bubble

private struct SupportBubbleView: View {
    let message: SupportMessage

    private var isFromAdmin: Bool { message.senderKind == .admin }

    var body: some View {
        HStack(alignment: .bottom, spacing: 8) {
            if !isFromAdmin { Spacer(minLength: 60) }

            VStack(alignment: isFromAdmin ? .leading : .trailing, spacing: 4) {
                if isFromAdmin {
                    Label("Support", systemImage: "headphones.circle.fill")
                        .font(.caption2.bold())
                        .foregroundColor(.driveBaiPrimary)
                }

                ForEach(message.attachments) { att in
                    SupportAttachmentView(attachment: att, isFromAdmin: isFromAdmin)
                }

                if message.hasText {
                    Text(message.body)
                        .font(.body)
                        .foregroundColor(isFromAdmin ? .primary : .white)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 10)
                        .background(isFromAdmin ? Color.driveBaiPrimary.opacity(0.12) : Color.driveBaiPrimary)
                        .clipShape(RoundedRectangle(cornerRadius: 18))
                        .overlay(
                            RoundedRectangle(cornerRadius: 18)
                                .stroke(
                                    isFromAdmin ? Color.driveBaiPrimary.opacity(0.25) : Color.clear,
                                    lineWidth: 1
                                )
                        )
                }

                Text(formatTime(message.createdAt))
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }

            if isFromAdmin { Spacer(minLength: 60) }
        }
    }

    private func formatTime(_ date: Date) -> String {
        let f = DateFormatter()
        f.dateFormat = "h:mm a"
        return f.string(from: date)
    }
}

// MARK: - Attachment bubble

/// Renders one in-chat attachment: images inline, other files as a tappable
/// tile that opens the signed URL. The URL is already signed by the server.
private struct SupportAttachmentView: View {
    let attachment: SupportAttachmentAPI
    let isFromAdmin: Bool

    private var url: URL? { URL(string: AppConfig.serverBaseURL.absoluteString + attachment.fileUrl) }

    var body: some View {
        if attachment.isImage, let url {
            RemoteImage(url: url, contentMode: .fill, maxPixelSize: 600)
                .frame(width: 200, height: 200)
                .clipShape(RoundedRectangle(cornerRadius: 16))
                .overlay(
                    RoundedRectangle(cornerRadius: 16)
                        .stroke(Color(.systemGray4), lineWidth: 0.5)
                )
        } else if let url {
            Link(destination: url) {
                HStack(spacing: 10) {
                    Image(systemName: "doc.fill").font(.system(size: 22))
                    VStack(alignment: .leading, spacing: 2) {
                        Text(attachment.mimeType == "application/pdf" ? "PDF document" : "Attachment")
                            .font(.subheadline.weight(.semibold))
                        Text("Tap to open").font(.caption2).foregroundColor(.secondary)
                    }
                }
                .foregroundColor(isFromAdmin ? .primary : .white)
                .padding(.horizontal, 14).padding(.vertical, 12)
                .background(isFromAdmin ? Color.driveBaiPrimary.opacity(0.12) : Color.driveBaiPrimary)
                .clipShape(RoundedRectangle(cornerRadius: 16))
            }
        }
    }
}

// MARK: - Tip Row

private struct SupportTipRow: View {
    let icon: String
    let text: String

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: icon)
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 20)
            Text(text)
                .font(.subheadline)
                .foregroundColor(.secondary)
        }
    }
}
