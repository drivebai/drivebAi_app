import SwiftUI

// MARK: - Support hub
//
// The "Ask for Help" entry, now a chooser rather than a bare chat: start a
// structured request (ticket), see requests you've sent, or just chat. Presented
// as a sheet from the floating button and Profile.
struct SupportHubView: View {
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @Environment(\.dismiss) private var dismiss
    @State private var showTicketFlow = false
    @State private var showChat = false
    @State private var requestsRefresh = 0

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Button { showTicketFlow = true } label: {
                        hubRow("plus.bubble.fill", "Start a request",
                               "Tell us what's wrong, attach evidence, and track it")
                    }
                    NavigationLink {
                        MyRequestsView(refreshToken: requestsRefresh)
                    } label: {
                        hubRow("list.bullet.rectangle.portrait.fill", "My requests",
                               "See the status of requests you've sent")
                    }
                    Button { showChat = true } label: {
                        hubRow("bubble.left.and.bubble.right.fill", "Chat with us",
                               "Quick questions — no request needed",
                               showUnread: supportInboxStore.unreadCount > 0)
                    }
                }
                Section {
                    Text("Support is a small team — we read every request and reply as soon as we can.")
                        .font(.footnote).foregroundColor(.secondary)
                }
            }
            .navigationTitle("Help & Support")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Close") { dismiss() } }
            }
        }
        .sheet(isPresented: $showTicketFlow) {
            TicketFlowView(onSubmitted: { requestsRefresh += 1 })
        }
        .sheet(isPresented: $showChat) {
            SupportChatView().environmentObject(supportInboxStore)
        }
    }

    // showUnread paints the same red dot the floating support button carries
    // (SupportInboxStore.unreadCount) onto a hub row, so the trail from the
    // floating button continues into the hub. Reading the chat calls
    // markRead(), which zeroes unreadCount and clears both at once.
    private func hubRow(_ icon: String, _ title: String, _ subtitle: String, showUnread: Bool = false) -> some View {
        HStack(spacing: 14) {
            Image(systemName: icon)
                .font(.system(size: 20))
                .foregroundColor(.driveBaiPrimary)
                .frame(width: 30)
                .overlay(alignment: .topTrailing) {
                    if showUnread {
                        Circle()
                            .fill(Color.red)
                            .frame(width: 10, height: 10)
                            .overlay(Circle().stroke(Color(.systemBackground), lineWidth: 1.5))
                            .offset(x: 5, y: -2)
                    }
                }
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.body.weight(.medium)).foregroundColor(.primary)
                Text(subtitle).font(.caption).foregroundColor(.secondary)
            }
            Spacer()
            Image(systemName: "chevron.right").font(.caption).foregroundColor(.secondary)
        }
        .padding(.vertical, 4)
    }
}

// MARK: - My requests (ticket history)

struct MyRequestsView: View {
    let refreshToken: Int
    @State private var tickets: [TicketAPIResponse] = []
    @State private var isLoading = true
    @State private var error: String?
    @State private var resumeDraft = false

    var body: some View {
        Group {
            if isLoading && tickets.isEmpty {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if tickets.isEmpty {
                emptyState
            } else {
                List {
                    ForEach(tickets) { ticket in
                        if ticket.statusEnum == .draft {
                            Button { resumeDraft = true } label: { TicketRow(ticket: ticket) }
                                .buttonStyle(.plain)
                        } else {
                            NavigationLink { TicketDetailView(ticketId: ticket.id) } label: {
                                TicketRow(ticket: ticket)
                            }
                        }
                    }
                }
            }
        }
        .navigationTitle("My requests")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await load() }
        .task(id: refreshToken) { await load() }
        .sheet(isPresented: $resumeDraft, onDismiss: { Task { await load() } }) {
            TicketFlowView(onSubmitted: { Task { await load() } })
        }
    }

    private var emptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "tray").font(.system(size: 44)).foregroundColor(.secondary)
            Text("No requests yet").font(.headline)
            Text("When you start a request, it'll show up here with its status.")
                .font(.subheadline).foregroundColor(.secondary)
                .multilineTextAlignment(.center).padding(.horizontal, 32)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func load() async {
        isLoading = true
        do {
            tickets = try await APIClient.shared.listTickets().tickets
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }
}

private struct TicketRow: View {
    let ticket: TicketAPIResponse

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(ticket.categoryEnum?.label ?? "Request")
                    .font(.subheadline.weight(.semibold))
                Spacer()
                TicketStatusChip(status: ticket.statusEnum)
            }
            Text(ticket.subject.isEmpty ? ticket.description : ticket.subject)
                .font(.caption).foregroundColor(.secondary).lineLimit(2)
            Text((ticket.submittedAt ?? ticket.createdAt).formatted(date: .abbreviated, time: .shortened))
                .font(.caption2).foregroundColor(.secondary)
        }
        .padding(.vertical, 2)
    }
}

struct TicketStatusChip: View {
    let status: TicketStatus
    var body: some View {
        Text(status.label)
            .font(.caption2.weight(.semibold))
            .foregroundColor(status.color)
            .padding(.horizontal, 8).padding(.vertical, 3)
            .background(Capsule().fill(status.color.opacity(0.15)))
    }
}

// MARK: - Ticket detail

struct TicketDetailView: View {
    let ticketId: UUID
    @EnvironmentObject private var supportInboxStore: SupportInboxStore
    @State private var ticket: TicketAPIResponse?
    @State private var isLoading = true
    @State private var isReopening = false
    @State private var showChat = false
    @State private var error: String?

    var body: some View {
        ScrollView {
            if let t = ticket {
                VStack(alignment: .leading, spacing: 18) {
                    HStack {
                        Text(t.categoryEnum?.label ?? "Request").font(.title3).fontWeight(.bold)
                        Spacer()
                        TicketStatusChip(status: t.statusEnum)
                    }
                    if !t.subject.isEmpty { detailRow("Subject", t.subject) }
                    detailRow("Details", t.description.isEmpty ? "—" : t.description)

                    if !t.attachments.isEmpty {
                        VStack(alignment: .leading, spacing: 8) {
                            Text("Evidence").font(.caption.weight(.medium)).foregroundColor(.secondary)
                            LazyVGrid(columns: [GridItem(.adaptive(minimum: 90), spacing: 10)], spacing: 10) {
                                ForEach(t.attachments) { att in
                                    EvidenceThumb(attachment: att)
                                }
                            }
                        }
                    }

                    detailRow("Submitted", (t.submittedAt ?? t.createdAt).formatted(date: .abbreviated, time: .shortened))
                    if let r = t.resolvedAt { detailRow("Resolved", r.formatted(date: .abbreviated, time: .shortened)) }

                    Button { showChat = true } label: {
                        Label("Message support about this", systemImage: "bubble.left.and.bubble.right.fill")
                            .font(.subheadline.weight(.semibold))
                            .frame(maxWidth: .infinity).padding(.vertical, 12)
                    }
                    .buttonStyle(DriveBaiButtonStyle())

                    if t.statusEnum == .resolved {
                        Button { Task { await reopen() } } label: {
                            HStack {
                                if isReopening { ProgressView() }
                                Text("Reopen — this isn't fixed")
                            }
                            .font(.subheadline.weight(.medium)).frame(maxWidth: .infinity).padding(.vertical, 10)
                        }
                        .buttonStyle(.bordered).tint(.orange)
                        .disabled(isReopening)
                    }
                }
                .padding(16)
            } else if isLoading {
                ProgressView().padding(40)
            }
        }
        .navigationTitle("Request").navigationBarTitleDisplayMode(.inline)
        .task { await load() }
        .sheet(isPresented: $showChat) { SupportChatView().environmentObject(supportInboxStore) }
        .alert("Something went wrong", isPresented: Binding(
            get: { error != nil }, set: { if !$0 { error = nil } }
        )) { Button("OK", role: .cancel) {} } message: { Text(error ?? "") }
    }

    private func detailRow(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(label).font(.caption.weight(.medium)).foregroundColor(.secondary)
            Text(value).font(.body).foregroundColor(.primary).fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func load() async {
        isLoading = true
        do { ticket = try await APIClient.shared.getTicket(id: ticketId) }
        catch let err { error = err.localizedDescription }
        isLoading = false
    }

    private func reopen() async {
        isReopening = true
        do { ticket = try await APIClient.shared.reopenTicket(id: ticketId) }
        catch let err { error = err.localizedDescription }
        isReopening = false
    }
}

private struct EvidenceThumb: View {
    let attachment: TicketAttachmentAPI
    private var isImage: Bool { attachment.mimeType.hasPrefix("image/") }
    private var url: URL? { URL(string: AppConfig.serverBaseURL.absoluteString + attachment.fileUrl) }

    var body: some View {
        Group {
            if isImage, let url {
                RemoteImage(url: url, contentMode: .fill, maxPixelSize: 300)
            } else {
                VStack(spacing: 4) {
                    Image(systemName: "doc.fill").font(.system(size: 22)).foregroundColor(.driveBaiPrimary)
                    Text("PDF").font(.caption2).foregroundColor(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Color(.systemGray6))
            }
        }
        .frame(width: 90, height: 90)
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }
}
