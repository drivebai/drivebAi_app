import SwiftUI

struct ChatRowView: View {
    let chat: ChatSummary

    var body: some View {
        HStack(spacing: 12) {
            // Car thumbnail
            if let url = chat.carCoverFullURL {
                RemoteImage(url: url)
                    .frame(width: 50, height: 50)
                    .clipShape(RoundedRectangle(cornerRadius: 8))
            } else {
                RoundedRectangle(cornerRadius: 8)
                    .fill(Color.driveBaiPrimary.opacity(0.1))
                    .frame(width: 50, height: 50)
                    .overlay(
                        Image(systemName: "car.fill")
                            .foregroundColor(.driveBaiPrimary.opacity(0.5))
                    )
            }

            // Name + last message
            VStack(alignment: .leading, spacing: 4) {
                Text(chat.counterpartyName)
                    .font(.headline)
                    .lineLimit(1)

                Text(chat.carTitle)
                    .font(.caption)
                    .foregroundColor(.driveBaiPrimary)
                    .lineLimit(1)

                if let preview = chat.lastMessage {
                    Text(preview)
                        .font(.subheadline)
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                }
            }

            Spacer()

            // Time + badges
            VStack(alignment: .trailing, spacing: 6) {
                if let date = chat.lastMessageAt {
                    Text(formatTimestamp(date))
                        .font(.caption2)
                        .foregroundColor(.secondary)
                }

                HStack(spacing: 4) {
                    if chat.unreadCount > 0 {
                        badge(count: chat.unreadCount, color: .driveBaiPrimary)
                    }
                    if chat.openRequestsCount > 0 {
                        badge(count: chat.openRequestsCount, color: .orange)
                    }
                }
            }
        }
        .padding(.vertical, 4)
    }

    private func badge(count: Int, color: Color) -> some View {
        Text("\(count)")
            .font(.caption2.bold())
            .foregroundColor(.white)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color)
            .clipShape(Capsule())
    }

    private func formatTimestamp(_ date: Date) -> String {
        let calendar = Calendar.current
        if calendar.isDateInToday(date) {
            let formatter = DateFormatter()
            formatter.dateFormat = "h:mm a"
            return formatter.string(from: date)
        } else if calendar.isDateInYesterday(date) {
            return "Yesterday"
        } else {
            let formatter = DateFormatter()
            formatter.dateFormat = "MMM d"
            return formatter.string(from: date)
        }
    }
}
