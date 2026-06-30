import SwiftUI
import UserNotifications

/// Notifications screen opened from the bell icon.
struct NotificationsView: View {
    let notifications: [NotificationItem]
    /// Called when the user taps a notification's action button.
    /// Receives the related chat ID if available.
    var onOpen: ((UUID?) -> Void)?
    var onMarkRead: ((UUID) -> Void)?
    var onMarkAllRead: (() -> Void)?

    @ObservedObject private var pushCoordinator = PushNotificationCoordinator.shared

    var body: some View {
        ScrollView {
            LazyVStack(spacing: 0) {
                // When the user declined the OS push prompt (or revoked
                // permission later in Settings), surface a yellow banner
                // with a one-tap deep-link to the app's Settings screen.
                // Without this affordance the only path back was for the
                // user to remember the OS settings flow themselves —
                // realistically nobody ever did.
                if pushCoordinator.authorizationStatus == .denied {
                    pushDisabledBanner
                }

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
        .task {
            // Re-poll the OS in case the user toggled permission from
            // Settings between opens — published value updates, banner
            // appears/disappears immediately.
            await pushCoordinator.refreshAuthorizationStatus()
        }
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

    /// Yellow banner shown when push permission is denied — recovery path
    /// for users who tapped "Don't Allow" at first run.
    private var pushDisabledBanner: some View {
        HStack(spacing: 12) {
            Image(systemName: "bell.slash.fill")
                .font(.system(size: 18))
                .foregroundColor(.orange)
            VStack(alignment: .leading, spacing: 2) {
                Text("Push notifications are off")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(.primary)
                Text("You won't get alerts when you're outside the app.")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            Spacer(minLength: 8)
            Button("Enable") {
                openAppSettings()
            }
            .font(.subheadline.weight(.semibold))
            .foregroundColor(.orange)
        }
        .padding(.vertical, 12)
        .padding(.horizontal, 16)
        .background(Color.orange.opacity(0.12))
    }

    private func openAppSettings() {
        guard let url = URL(string: UIApplication.openSettingsURLString) else { return }
        UIApplication.shared.open(url)
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
