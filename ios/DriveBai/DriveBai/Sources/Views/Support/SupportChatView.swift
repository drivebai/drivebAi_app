import SwiftUI

struct SupportChatView: View {
    @StateObject private var viewModel = SupportChatViewModel()
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @Environment(\.dismiss) private var dismiss

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

            Text("Tell us what happened and our support team will respond as soon as possible.")
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
        HStack(alignment: .bottom, spacing: 12) {
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
