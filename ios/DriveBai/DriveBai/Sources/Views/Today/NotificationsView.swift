import SwiftUI

/// Notifications screen opened from the bell icon.
struct NotificationsView: View {
    let notifications: [NotificationItem]
    /// Called when the user taps a notification's action button.
    /// Receives the related chat ID if available.
    var onOpen: ((UUID?) -> Void)?
    var onMarkRead: ((UUID) -> Void)?
    var onMarkAllRead: (() -> Void)?

    var body: some View {
        ScrollView {
            LazyVStack(spacing: 0) {
                if notifications.isEmpty {
                    emptyState
                } else {
                    ForEach(notifications) { notification in
                        NotificationRow(notification: notification) {
                            onMarkRead?(notification.id)
                            onOpen?(notification.relatedChatId)
                        }
                        if notification.id != notifications.last?.id {
                            Divider()
                                .padding(.leading, 76)
                        }
                    }
                }
            }
            .background(Color(.systemBackground))
        }
        .background(Color(.systemGray6))
        .navigationTitle("Notifications")
        .navigationBarTitleDisplayMode(.large)
        .toolbar {
            if notifications.contains(where: { !$0.isRead }) {
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("Mark all read") {
                        onMarkAllRead?()
                    }
                    .font(.subheadline)
                }
            }
        }
    }

    private var emptyState: some View {
        VStack(spacing: 16) {
            Spacer(minLength: 60)
            Image(systemName: "bell.slash")
                .font(.system(size: 44))
                .foregroundColor(.gray.opacity(0.4))
            Text("No notifications yet")
                .font(.headline)
                .foregroundColor(.secondary)
            Spacer(minLength: 60)
        }
        .frame(maxWidth: .infinity)
        .padding()
    }
}

/// Individual notification row.
private struct NotificationRow: View {
    let notification: NotificationItem
    let onTap: () -> Void

    var body: some View {
        Button(action: onTap) {
            HStack(alignment: .top, spacing: 12) {
                RoundedRectangle(cornerRadius: 8)
                    .fill(Color(.systemGray5))
                    .frame(width: 52, height: 52)
                    .overlay(
                        Image(systemName: notification.type.iconName)
                            .font(.system(size: 20))
                            .foregroundColor(.gray)
                    )

                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text(notification.type.rawValue.uppercased())
                            .font(.caption)
                            .fontWeight(.semibold)
                            .foregroundColor(.secondary)
                        Spacer()
                        Text(notification.formattedDate.uppercased())
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }

                    Text(notification.title)
                        .font(.subheadline)
                        .fontWeight(.medium)
                        .foregroundColor(.primary)

                    Text(notification.body)
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .lineLimit(2)
                }

                if notification.relatedChatId != nil {
                    Image(systemName: "arrow.up.right.square")
                        .font(.system(size: 18))
                        .foregroundColor(.gray)
                        .padding(.top, 4)
                }
            }
            .padding()
            .background(
                notification.isRead ? Color.clear : Color.driveBaiPrimary.opacity(0.05)
            )
        }
        .buttonStyle(.plain)
    }
}

#Preview {
    NavigationStack {
        NotificationsView(notifications: NotificationItem.mockNotifications())
    }
}

#Preview("Empty") {
    NavigationStack {
        NotificationsView(notifications: [])
    }
}
