import SwiftUI

/// Notifications screen opened from the bell icon
/// Displays a list of notification items matching the Figma design
struct NotificationsView: View {
    let notifications: [NotificationItem]

    var body: some View {
        ScrollView {
            LazyVStack(spacing: 0) {
                ForEach(notifications) { notification in
                    NotificationRow(notification: notification)

                    if notification.id != notifications.last?.id {
                        Divider()
                            .padding(.leading, 76)
                    }
                }
            }
            .background(Color(.systemBackground))
        }
        .background(Color(.systemGray6))
        .navigationTitle("Notifications")
        .navigationBarTitleDisplayMode(.large)
    }
}

/// Individual notification row matching Figma design
private struct NotificationRow: View {
    let notification: NotificationItem

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            // Leading square placeholder
            RoundedRectangle(cornerRadius: 8)
                .fill(Color(.systemGray5))
                .frame(width: 52, height: 52)
                .overlay(
                    Image(systemName: notification.type.iconName)
                        .font(.system(size: 20))
                        .foregroundColor(.gray)
                )

            // Content
            VStack(alignment: .leading, spacing: 4) {
                // Type and date row
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

                // Title
                Text(notification.title)
                    .font(.subheadline)
                    .fontWeight(.medium)
                    .foregroundColor(.primary)

                // Body
                Text(notification.body)
                    .font(.subheadline)
                    .foregroundColor(.secondary)
                    .lineLimit(2)
            }

            // Trailing external link icon
            Button(action: {
                print("Open notification: \(notification.id)")
            }) {
                Image(systemName: "arrow.up.right.square")
                    .font(.system(size: 18))
                    .foregroundColor(.gray)
            }
            .padding(.top, 4)
        }
        .padding()
        .background(
            notification.isRead ? Color.clear : Color.driveBaiPrimary.opacity(0.05)
        )
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
