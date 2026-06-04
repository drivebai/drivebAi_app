import SwiftUI

struct ChatDetailsView: View {
    let chatId: UUID

    @StateObject private var viewModel: ChatDetailsViewModel
    @State private var keyHandover: KeyHandover?

    init(chatId: UUID) {
        self.chatId = chatId
        _viewModel = StateObject(wrappedValue: ChatDetailsViewModel(chatId: chatId))
    }

    var body: some View {
        Group {
            if viewModel.isLoading && viewModel.details == nil {
                ProgressView()
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let details = viewModel.details {
                detailsContent(details)
            } else if let error = viewModel.error {
                VStack(spacing: 12) {
                    Image(systemName: "exclamationmark.triangle")
                        .font(.largeTitle)
                        .foregroundColor(.secondary)
                    Text(error)
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                    Button("Retry") {
                        Task { await viewModel.loadDetails() }
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .navigationTitle("Chat Details")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            await viewModel.loadDetails()
            await viewModel.loadAttachments()
            await loadKeyHandover()
        }
    }

    /// Resolve an active key handover linked to this chat, if any.
    private func loadKeyHandover() async {
        if let list = try? await APIClient.shared.fetchKeyHandoversToday() {
            keyHandover = list.keyHandovers
                .map { $0.toDomain() }
                .first { $0.chatId == chatId }
        }
    }

    private func detailsContent(_ details: ChatDetails) -> some View {
        List {
            // Car info section
            Section("Car") {
                HStack(spacing: 12) {
                    if let url = ImageURLHelper.fullURL(for: details.car.coverPhotoURL) {
                        RemoteImage(url: url, maxPixelSize: 240)
                            .frame(width: 60, height: 60)
                            .clipShape(RoundedRectangle(cornerRadius: 8))
                    } else {
                        RoundedRectangle(cornerRadius: 8)
                            .fill(Color(.systemGray5))
                            .frame(width: 60, height: 60)
                            .overlay(
                                Image(systemName: "car.fill")
                                    .foregroundColor(.gray)
                            )
                    }

                    VStack(alignment: .leading, spacing: 4) {
                        Text(details.car.title)
                            .font(.headline)
                        if let price = details.car.weeklyRentPrice {
                            Text("\(details.car.currency) \(price, specifier: "%.0f") / week")
                                .font(.subheadline)
                                .foregroundColor(.secondary)
                        }
                        Text(details.car.status.capitalized)
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }
            }

            // Counterparty section
            Section("Counterparty") {
                HStack(spacing: 12) {
                    if let url = ImageURLHelper.fullURL(for: details.counterparty.avatarURL) {
                        RemoteImage(url: url, maxPixelSize: 180)
                            .frame(width: 44, height: 44)
                            .clipShape(Circle())
                    } else {
                        Circle()
                            .fill(Color(.systemGray5))
                            .frame(width: 44, height: 44)
                            .overlay(
                                Image(systemName: "person.fill")
                                    .foregroundColor(.gray)
                            )
                    }

                    VStack(alignment: .leading, spacing: 2) {
                        Text(details.counterparty.name)
                            .font(.headline)
                        Text("\(details.counterparty.role.capitalized) since \(formatDate(details.counterparty.memberSince))")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }
            }

            // Settings section
            Section("Settings") {
                Toggle("Auto-translate Messages", isOn: Binding(
                    get: { details.autoTranslateEnabled },
                    set: { _ in Task { await viewModel.toggleAutoTranslation() } }
                ))

                Toggle("Mute Notifications", isOn: Binding(
                    get: { details.notificationsMuted },
                    set: { _ in Task { await viewModel.toggleMuteNotifications() } }
                ))
            }

            // Shared media section
            Section("Shared Files") {
                HStack {
                    Label("Documents", systemImage: "doc.fill")
                    Spacer()
                    Text("\(details.documentsCount)")
                        .foregroundColor(.secondary)
                }

                HStack {
                    Label("Media", systemImage: "photo.fill")
                    Spacer()
                    Text("\(details.mediaCount)")
                        .foregroundColor(.secondary)
                }
            }

            // Documents list
            if !viewModel.documents.isEmpty {
                Section("Recent Documents") {
                    ForEach(viewModel.documents.prefix(5)) { doc in
                        HStack {
                            Image(systemName: "doc.fill")
                                .foregroundColor(.blue)
                            Text(doc.filename)
                                .font(.subheadline)
                                .lineLimit(1)
                            Spacer()
                            Text(formatFileSize(doc.fileSize))
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
                }
            }

            // Media list
            if !viewModel.media.isEmpty {
                Section("Recent Media") {
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 80))], spacing: 8) {
                        ForEach(viewModel.media.prefix(9)) { item in
                            if let url = item.fullFileURL {
                                RemoteImage(url: url, maxPixelSize: 320)
                                    .frame(width: 80, height: 80)
                                    .clipShape(RoundedRectangle(cornerRadius: 6))
                            }
                        }
                    }
                }
            }

            // Key handover (only when an active handover is linked to this chat)
            if let handover = keyHandover {
                Section("Key handover") {
                    NavigationLink {
                        KeyHandoverDetailView(handover: handover)
                    } label: {
                        HStack(spacing: 12) {
                            Label("Key handover", systemImage: "key.fill")
                                .foregroundColor(.driveBaiPrimary)
                            Spacer()
                            Text(handover.isOwner && handover.status == .pending ? "Action needed"
                                 : (!handover.isOwner && handover.status == .ownerConfirmed ? "Action needed" : "View"))
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
                }
            }

            // Info section
            Section("Info") {
                HStack {
                    Text("Chat created")
                    Spacer()
                    Text(formatDate(details.createdAt))
                        .foregroundColor(.secondary)
                }
            }

        }
    }

    private func formatDate(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.dateStyle = .medium
        return formatter.string(from: date)
    }

    private func formatFileSize(_ bytes: Int) -> String {
        if bytes < 1024 { return "\(bytes) B" }
        if bytes < 1024 * 1024 { return "\(bytes / 1024) KB" }
        return String(format: "%.1f MB", Double(bytes) / (1024 * 1024))
    }
}
